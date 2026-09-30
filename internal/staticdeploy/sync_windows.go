package staticdeploy

// syncDirectory 保持 Windows 本地开发兼容；Windows 不支持普通目录句柄的 File.Sync。
func syncDirectory(string) error { return nil }
