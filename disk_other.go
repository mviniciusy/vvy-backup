//go:build !linux

package main

import "errors"

// diskUsage só existe no Linux; no Windows o app roda apenas para testar a interface.
func diskUsage(string) (int64, int64, error) { return 0, 0, errors.New("disponível só no Linux") }
