//go:build !windows

package foundation

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// RedirectStdToLogFile 把进程 stdout/stderr 的底层文件描述符（fd 1/2）接管，转发进
// opts.Dir/opts.Filename 这份带轮转的日志文件——用来取代 run.sh 里 nohup 单独重定向
// 到的、永不轮转的 server.out.log。用 unix.Dup2 在 fd 层面接管而不是替换
// os.Stdout/os.Stderr 变量，这样才能连带捕获 panic 输出、logger 初始化之前的
// fmt.Fprintf(os.Stderr, ...) 以及任何第三方依赖直接写 fd 的内容，不止是经由
// os.Stdout 变量写入的部分。opts.File 为 false（未启用文件日志）时不做任何事。
// 用 golang.org/x/sys/unix 而不是标准库 syscall：标准库的 Dup2 在 linux/arm64
// 上不存在（该架构的 Linux 内核只有 dup3，标准库没有补一个基于 dup3 的 Dup2
// 包装），x/sys/unix 的 Dup2 在各 Unix 平台/架构上都统一可用。
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
	if err := unix.Dup2(int(pw.Fd()), int(os.Stdout.Fd())); err != nil {
		return err
	}
	if err := unix.Dup2(int(pw.Fd()), int(os.Stderr.Fd())); err != nil {
		return err
	}
	pw.Close() // fd 1/2 已各自持有一份副本，这里关掉的只是多余的第三份

	go io.Copy(fileWriter, pr)
	return nil
}
