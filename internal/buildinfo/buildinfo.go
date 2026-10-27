package buildinfo

// Version 和 Revision 在发布构建时注入；本地源码构建如实标为 dev / unknown。
var (
	Version  = "dev"
	Revision = "unknown"
)

// Info 是可向管理员展示的构建身份，不包含运行环境或配置。
type Info struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
}

// Current 返回编译进程序的版本，不读取用户可修改的运行时环境变量。
func Current() Info {
	return Info{Version: Version, Revision: Revision}
}
