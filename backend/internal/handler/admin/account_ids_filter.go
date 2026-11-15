package admin

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

// parseAccountListIDs keeps the existing export parser's ID syntax while
// requiring nonempty list entries and limiting the unique selection to 1000.
func parseAccountListIDs(c *gin.Context) ([]int64, error) {
	for _, value := range c.QueryArray("ids") {
		for _, part := range strings.Split(value, ",") {
			if strings.TrimSpace(part) == "" {
				return nil, fmt.Errorf("ids must contain positive account IDs")
			}
		}
	}
	ids, err := parseAccountIDs(c)
	if err != nil || ids == nil {
		return nil, err
	}
	seen := make(map[int64]struct{}, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
		if len(unique) > 1000 {
			return nil, fmt.Errorf("ids must contain at most 1000 unique account IDs")
		}
	}
	return unique, nil
}
