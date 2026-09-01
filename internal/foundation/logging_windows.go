//go:build windows

package foundation

import (
	"io"
	"os"
)

// RedirectStdToLogFile 是 RedirectStdToLogFile 的 Windows 实现。Unix 版本用
// unix.Dup2 在文件描述符层面接管 fd 1/2，因此连 Go runtime 崩溃时直接系统调用
// 写 fd 2 的 panic 输出都能捕获；Windows 没有 POSIX 文件描述符语义，做不到同等
// 力度的接管，这里退而求其次，直接替换 os.Stdout/os.Stderr 这两个包级变量——
// 能覆盖本项目自身代码里所有经由 fmt.Println/fmt.Fprintf(os.Stdout|os.Stderr,
// ...) 的输出（包括 main.go 里 logger 初始化之前的错误信息），但无法捕获 Go
// runtime 在 panic 时绕过这两个变量、直接写系统标准错误句柄的那部分内容——
// 这种情况下 Windows 用户仍需要看 Windows 服务管理器/控制台窗口本身的输出。
func RedirectStdToLogFile(opts LogOptions) error {
	if !opts.File {
		return nil
	}
	fileWriter, err := newRedirectFileWriter(opts)
	if err != nil {
		return err
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	os.Stdout = pw
	os.Stderr = pw

	go io.Copy(fileWriter, pr)
	return nil
}
