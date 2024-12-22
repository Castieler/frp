// Copyright 2018 fatedier, fatedier@gmail.com
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sub

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/config"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	"github.com/fatedier/frp/pkg/util/log"
	"github.com/fatedier/frp/pkg/util/version"
)

var (
	cfgFile          string // 配置文件路径
	cfgDir           string // 配置目录路径
	showVersion      bool   // 是否显示版本信息
	strictConfigMode bool   // 严格配置模式，未知字段将导致错误
)

func init() {
	// 初始化命令行标志
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "./frpc.ini", "frpc的配置文件")
	rootCmd.PersistentFlags().StringVarP(&cfgDir, "config_dir", "", "", "配置目录，为配置目录中的每个文件运行一个frpc服务")
	rootCmd.PersistentFlags().BoolVarP(&showVersion, "version", "v", false, "frpc的版本")
	rootCmd.PersistentFlags().BoolVarP(&strictConfigMode, "strict_config", "", true, "严格配置解析模式，未知字段将导致错误")
}

var rootCmd = &cobra.Command{
	Use:   "frpc",                                           // 命令的使用方法
	Short: "frpc是frp的客户端 (https://github.com/fatedier/frp)", // 命令的简短描述
	RunE: func(cmd *cobra.Command, args []string) error {
		if showVersion {
			// 如果需要显示版本信息，打印版本并返回
			fmt.Println(version.Full())
			return nil
		}

		// 如果cfgDir不为空，为cfgDir中的每个配置文件运行多个frpc服务
		// 注意，这仅用于测试，不能保证稳定性
		if cfgDir != "" {
			_ = runMultipleClients(cfgDir)
			return nil
		}

		// 不显示命令用法
		err := runClient(cfgFile)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		return nil
	},
}

func runMultipleClients(cfgDir string) error {
	var wg sync.WaitGroup
	// 遍历配置目录中的每个文件
	err := filepath.WalkDir(cfgDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // 如果是目录或出错，跳过
		}
		wg.Add(1)
		time.Sleep(time.Millisecond) // 短暂休眠
		go func() {
			defer wg.Done()
			err := runClient(path) // 为每个配置文件运行客户端
			if err != nil {
				fmt.Printf("配置文件 [%s] 的frpc服务出错\n", path)
			}
		}()
		return nil
	})
	wg.Wait() // 等待所有goroutine完成
	return err
}

func Execute() {
	// 设置全局命令规范化函数
	rootCmd.SetGlobalNormalizationFunc(config.WordSepNormalizeFunc)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1) // 如果执行出错，退出程序
	}
}

func handleTermSignal(svr *client.Service) {
	ch := make(chan os.Signal, 1)
	// 监听系统中断信号
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	// 优雅关闭服务
	svr.GracefulClose(500 * time.Millisecond)
}

func runClient(cfgFilePath string) error {
	// 加载客户端配置
	cfg, proxyCfgs, visitorCfgs, isLegacyFormat, err := config.LoadClientConfig(cfgFilePath, strictConfigMode)
	if err != nil {
		return err
	}
	if isLegacyFormat {
		// 如果是旧格式，打印警告
		fmt.Printf("警告: ini格式已弃用，未来将移除支持，请使用yaml/json/toml格式！\n")
	}

	// 验证所有客户端配置
	warning, err := validation.ValidateAllClientConfig(cfg, proxyCfgs, visitorCfgs)
	if warning != nil {
		fmt.Printf("警告: %v\n", warning)
	}
	if err != nil {
		return err
	}
	return startService(cfg, proxyCfgs, visitorCfgs, cfgFilePath) // 启动服务
}

func startService(
	cfg *v1.ClientCommonConfig,
	proxyCfgs []v1.ProxyConfigurer,
	visitorCfgs []v1.VisitorConfigurer,
	cfgFile string,
) error {
	// 初始化日志
	log.InitLogger(cfg.Log.To, cfg.Log.Level, int(cfg.Log.MaxDays), cfg.Log.DisablePrintColor)

	if cfgFile != "" {
		// 记录服务启动和停止日志
		log.Infof("启动配置文件 [%s] 的frpc服务", cfgFile)
		defer log.Infof("配置文件 [%s] 的frpc服务已停止", cfgFile)
	}
	svr, err := client.NewService(client.ServiceOptions{
		Common:         cfg,
		ProxyCfgs:      proxyCfgs,
		VisitorCfgs:    visitorCfgs,
		ConfigFilePath: cfgFile,
	})
	if err != nil {
		return err
	}

	// 如果使用kcp或quic协议，捕获退出信号
	shouldGracefulClose := cfg.Transport.Protocol == "kcp" || cfg.Transport.Protocol == "quic"
	if shouldGracefulClose {
		go handleTermSignal(svr)
	}
	return svr.Run(context.Background()) // 运行服务
}
