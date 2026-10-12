package containerapp

import (
	"sort"
	"strings"
)

// redactLog 优先匹配完整长值并仅扫描一次，防止重叠凭据和替换标记二次改写。
func redactLog(text string, specs ...Spec) string {
	secrets := map[string]bool{}
	for _, spec := range specs {
		for _, value := range spec.Env {
			if value != "" {
				secrets[value] = true
			}
		}
		if spec.Registry != nil && spec.Registry.Password != "" {
			secrets[spec.Registry.Password] = true
		}
	}
	values := make([]string, 0, len(secrets))
	for value := range secrets {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) != len(values[j]) {
			return len(values[i]) > len(values[j])
		}
		return values[i] < values[j]
	})
	replacements := make([]string, 0, len(values)*2)
	for _, value := range values {
		replacements = append(replacements, value, "[已隐藏]")
	}
	return strings.NewReplacer(replacements...).Replace(text)
}
