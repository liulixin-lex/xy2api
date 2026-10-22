//go:build unit

package service

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var groupCategoryMention = regexp.MustCompile(`GroupCategor|GroupListCategor|group_categor`)

// TestGroupCategoriesStayOutOfRequestPath 分类只是后台标签：分组模型和鉴权快照没有分类字段，
// 网关、调度、计费和用户端代码不提分类，只有下面列出的文件能引用它。
func TestGroupCategoriesStayOutOfRequestPath(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(Group{}), reflect.TypeOf(APIKeyAuthGroupSnapshot{})} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			require.NotContains(t, strings.ToLower(field.Name+" "+field.Tag.Get("json")), "categor", "%s.%s", typ.Name(), field.Name)
		}
	}

	allowed := map[string][]string{
		".":              {"admin_account.go", "group_category.go", "wire.go"},
		"../repository":  {"account_repo.go", "group_category_repo.go", "group_repo.go", "wire.go"},
		"../handler":     {"handler.go", "wire.go"},
		"../handler/dto": nil,
	}
	for dir, files := range allowed {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err, dir)
		var mentions []string
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			content, err := os.ReadFile(filepath.Join(dir, name))
			require.NoError(t, err)
			if groupCategoryMention.Match(content) {
				mentions = append(mentions, name)
			}
		}
		sort.Strings(mentions)
		require.Equal(t, files, mentions, "files in %s that mention group categories", dir)
	}
}
