// authfile.go 账号凭证落盘：与 login.sh / login.ps1 产物格式完全一致
// （auths/workbuddy-<uid>.json，嵌套形，0600），复用 auth.Auth.SaveAtomic 的原子写。
package admin

import (
	"os"
	"path/filepath"

	"workbuddy2api/internal/auth"
)

// authFileName 凭证文件名（与 login 脚本同口径）。
func authFileName(uid string) string {
	return "workbuddy-" + uid + ".json"
}

// writeAuthFile 在 dir 下写入账号凭证（目录不存在则创建），返回写入路径。
func writeAuthFile(dir string, a *auth.Auth) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	a.FilePath = filepath.Join(dir, authFileName(a.UID))
	return a.SaveAtomic()
}
