// 版权声明，说明该文件的版权信息
// Licensed under the Apache License, Version 2.0 (the "License");
// 需要遵循该许可证的条款和条件

package main // 定义包名为 main

import (
	"context" // 导入上下文包，用于控制 goroutine 的生命周期
	"fmt"     // 导入格式化 I/O 包
	"os"      // 导入操作系统功能包

	"github.com/spf13/cobra" // 导入 cobra 包，用于构建命令行应用

	"github.com/fatedier/frp/pkg/config"               // 导入 frp 的配置包
	v1 "github.com/fatedier/frp/pkg/config/v1"         // 导入 frp 的 v1 版本配置包
	"github.com/fatedier/frp/pkg/config/v1/validation" // 导入配置验证包
	"github.com/fatedier/frp/pkg/util/log"             // 导入日志工具包
	"github.com/fatedier/frp/pkg/util/version"         // 导入版本工具包
	"github.com/fatedier/frp/server"                   // 导入 frp 的服务器包
)

var (
	cfgFile          string // 配置文件路径
	showVersion      bool   // 是否显示版本信息
	strictConfigMode bool   // 严格配置模式标志

	serverCfg v1.ServerConfig // 服务器配置结构体
)

func init() {
	// 初始化函数，用于设置命令行标志
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file of frps")                                                         // 配置文件路径标志
	rootCmd.PersistentFlags().BoolVarP(&showVersion, "version", "v", false, "version of frps")                                                       // 显示版本标志
	rootCmd.PersistentFlags().BoolVarP(&strictConfigMode, "strict_config", "", true, "strict config parsing mode, unknown fields will cause errors") // 严格配置模式标志

	config.RegisterServerConfigFlags(rootCmd, &serverCfg) // 注册服务器配置标志
}

var rootCmd = &cobra.Command{
	Use:   "frps",                                                        // 命令使用说明
	Short: "frps is the server of frp (https://github.com/fatedier/frp)", // 命令简短描述
	RunE: func(cmd *cobra.Command, args []string) error { // 命令执行的主函数
		if showVersion { // 如果需要显示版本信息
			fmt.Println(version.Full()) // 打印完整版本信息
			return nil                  // 退出
		}

		var (
			svrCfg         *v1.ServerConfig // 服务器配置指针
			isLegacyFormat bool             // 是否为旧格式标志
			err            error            // 错误信息
		)
		if cfgFile != "" { // 如果指定了配置文件
			svrCfg, isLegacyFormat, err = config.LoadServerConfig(cfgFile, strictConfigMode) // 加载服务器配置
			if err != nil {                                                                  // 如果加载出错
				fmt.Println(err) // 打印错误信息
				os.Exit(1)       // 退出程序
			}
			if isLegacyFormat { // 如果是旧格式
				fmt.Printf("WARNING: ini format is deprecated and the support will be removed in the future, " +
					"please use yaml/json/toml format instead!\n") // 打印警告信息
			}
		} else { // 如果没有指定配置文件
			serverCfg.Complete() // 完成默认配置
			svrCfg = &serverCfg  // 使用默认配置
		}

		warning, err := validation.ValidateServerConfig(svrCfg) // 验证服务器配置
		if warning != nil {                                     // 如果有警告
			fmt.Printf("WARNING: %v\n", warning) // 打印警告信息
		}
		if err != nil { // 如果有错误
			fmt.Println(err) // 打印错误信息
			os.Exit(1)       // 退出程序
		}

		if err := runServer(svrCfg); err != nil { // 运行服务器
			fmt.Println(err) // 打印错误信息
			os.Exit(1)       // 退出程序
		}
		return nil // 正常退出
	},
}

func Execute() {
	rootCmd.SetGlobalNormalizationFunc(config.WordSepNormalizeFunc) // 设置全局命令规范化函数
	if err := rootCmd.Execute(); err != nil {                       // 执行命令
		os.Exit(1) // 如果出错，退出程序
	}
}

func runServer(cfg *v1.ServerConfig) (err error) {
	log.InitLogger(cfg.Log.To, cfg.Log.Level, int(cfg.Log.MaxDays), cfg.Log.DisablePrintColor) // 初始化日志

	if cfgFile != "" { // 如果使用配置文件
		log.Infof("frps uses config file: %s", cfgFile) // 打印使用的配置文件信息
	} else { // 如果使用命令行参数
		log.Infof("frps uses command line arguments for config") // 打印使用命令行参数的信息
	}

	svr, err := server.NewService(cfg) // 创建新的服务器服务
	if err != nil {                    // 如果出错
		return err // 返回错误
	}
	log.Infof("frps started successfully") // 打印服务器启动成功信息
	svr.Run(context.Background())          // 运行服务器
	return                                 // 返回
}
