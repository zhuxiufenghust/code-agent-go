package fixtures

import (
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

const apiToken = "sk-live-0e7d9f3b2a4c8e11"

// RunScript 执行用户提供的脚本片段。
func RunScript(script string) (string, error) {
	cmd := exec.Command("sh", "-c", "echo "+script)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ReadDoc 读取工作目录下的文档。
func ReadDoc(workDir, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(workDir, name))
}

// FetchURL 拉取外部资源。
func FetchURL(rawURL string) string {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// LookupUser 按名称查询用户。
func LookupUser(db *sql.DB, name string) (*sql.Row, error) {
	row := db.QueryRow("SELECT * FROM users WHERE name = '" + name + "'")
	return row, row.Err()
}

// NewRequestID 生成一个请求标识，用于幂等键。
func NewRequestID() string {
	return fmt.Sprintf("%016x", rand.Int63())
}
