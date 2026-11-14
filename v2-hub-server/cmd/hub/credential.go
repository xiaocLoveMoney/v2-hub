package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

const (
	promptNew     = "新密码（12–128 个字符）: "
	promptConfirm = "再输入一次: "
)

var errCredentialMismatch = errors.New("两次输入的密码不一致")

// readNewCredential 读取新密码：stdin 是终端时无回显输入两次并比对；否则（管道 / 脚本）读第一行。
// 密码只在内存中传给 service，不打印、不写日志。
func readNewCredential(stdin *os.File, prompt io.Writer) (string, error) {
	fd := int(stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read credential from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	first, err := readHidden(fd, prompt, promptNew)
	if err != nil {
		return "", err
	}
	second, err := readHidden(fd, prompt, promptConfirm)
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errCredentialMismatch
	}
	return first, nil
}

func readHidden(fd int, prompt io.Writer, label string) (string, error) {
	if _, err := fmt.Fprint(prompt, label); err != nil {
		return "", fmt.Errorf("write prompt: %w", err)
	}
	raw, err := term.ReadPassword(fd)
	if _, perr := fmt.Fprintln(prompt); perr != nil {
		return "", fmt.Errorf("write prompt: %w", perr)
	}
	if err != nil {
		return "", fmt.Errorf("read credential: %w", err)
	}
	return string(raw), nil
}
