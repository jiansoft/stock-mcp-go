package stock

// 本檔案是 README 與實際註冊工具的一致性契約測試:兩份 README 的工具表格
// 必須與 AddTools 註冊的工具集合完全相同,新增工具卻漏改文件時測試會失敗。

import (
	"os"
	"regexp"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var readmeToolRow = regexp.MustCompile(`(?m)^\| \x60([a-z0-9_]+)\x60 \|`)

// readmeToolCount 是兩份 README 開頭功能清單裡寫的工具數。
var readmeToolCount = map[string]*regexp.Regexp{
	"../README.md":       regexp.MustCompile(`(?m)^- (\d+) tools `),
	"../README.zh-TW.md": regexp.MustCompile(`(?m)提供 (\d+) 個工具`),
}

// registeredToolNames 以 tools/list 取得實際註冊的工具名稱(已排序)。
func registeredToolNames(t *testing.T) []string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, NewAPIClient("http://127.0.0.1:1", "test-key", time.Second), nil)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), t1, nil); err != nil {
		t.Fatalf("server.Connect:%v", err)
	}
	cs, err := client.Connect(t.Context(), t2, nil)
	if err != nil {
		t.Fatalf("client.Connect:%v", err)
	}
	defer cs.Close()
	var names []string
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("列出工具:%v", err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func TestReadmeToolTablesMatchRegisteredTools(t *testing.T) {
	registered := registeredToolNames(t)
	for _, file := range []string{"../README.md", "../README.zh-TW.md"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("讀取 %s:%v", file, err)
		}
		var documented []string
		for _, m := range readmeToolRow.FindAllStringSubmatch(string(raw), -1) {
			documented = append(documented, m[1])
		}
		slices.Sort(documented)
		if m := readmeToolCount[file].FindStringSubmatch(string(raw)); m == nil || m[1] != strconv.Itoa(len(registered)) {
			t.Errorf("%s 開頭寫的工具數 %v 與註冊的 %d 個不一致", file, m, len(registered))
		}
		if !slices.Equal(documented, registered) {
			missing, extra := diffNames(registered, documented), diffNames(documented, registered)
			t.Errorf("%s 與註冊工具不一致:README 缺少 %v;README 多出 %v", file, missing, extra)
		}
	}
}

// diffNames 回傳在 a 但不在 b 的名稱。
func diffNames(a, b []string) []string {
	var out []string
	for _, n := range a {
		if !slices.Contains(b, n) {
			out = append(out, n)
		}
	}
	return out
}
