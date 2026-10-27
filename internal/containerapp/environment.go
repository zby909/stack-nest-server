package containerapp

// patchEnvironment 在副本上修改指定变量，nil 删除键，空字符串保留为空值。
func patchEnvironment(current map[string]string, patch map[string]*string) (map[string]string, error) {
	if len(patch) > 64 {
		return nil, invalid("一次最多修改 64 个环境变量")
	}
	result := make(map[string]string, len(current))
	for key, value := range current {
		result[key] = value
	}
	for key, value := range patch {
		if !envPattern.MatchString(key) || len(key) > 128 {
			return nil, invalid("环境变量名称无效")
		}
		if value == nil {
			delete(result, key)
		} else {
			result[key] = *value
		}
	}
	return result, nil
}
