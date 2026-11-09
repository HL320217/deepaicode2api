// Package googleauth drives DeepAI's Google sign-in in a visible local browser.
package googleauth

import (
	"fmt"
	"net/mail"
	"strings"
)

type Credential struct {
	Email    string
	Password string
}

// ParseAccounts accepts Google accounts on any domain. Passwords are split only
// once, so delimiters inside a password are preserved. Errors never echo lines.
func ParseAccounts(content string) ([]Credential, error) {
	var out []Credential
	positions := map[string]int{}
	for i, line := range strings.Split(strings.TrimPrefix(content, "\ufeff"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		var email, password string
		var ok bool
		separatorIndex, separatorLength := len(line), 0
		for _, separator := range []string{"----", "\t", "|"} {
			if index := strings.Index(line, separator); index >= 0 && index < separatorIndex {
				separatorIndex, separatorLength = index, len(separator)
			}
		}
		if separatorLength > 0 {
			email, password, ok = line[:separatorIndex], line[separatorIndex+separatorLength:], true
		}
		email = strings.ToLower(strings.TrimSpace(email))
		password = strings.TrimSpace(password)
		address, err := mail.ParseAddress(email)
		if !ok || err != nil || address.Address != email || address.Name != "" || !strings.Contains(email, "@") || strings.ContainsAny(email, " \t|<>\\\"") || len(email) > 254 || password == "" || len(password) > 1024 || strings.ContainsAny(password, "\x00\r\n") {
			return nil, fmt.Errorf("第 %d 行格式错误，请使用 邮箱----Google密码", i+1)
		}
		account := Credential{Email: email, Password: password}
		if index, exists := positions[email]; exists {
			out[index] = account
		} else {
			positions[email] = len(out)
			out = append(out, account)
		}
		if len(out) > 1000 {
			return nil, fmt.Errorf("一次最多导入 1000 个账号")
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("文件中没有有效账号")
	}
	return out, nil
}
