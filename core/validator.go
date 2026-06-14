package core

// defaultValidator 全局结构体验证器，由 goweb 包在初始化时注入。
var defaultValidator Validator

// SetDefaultValidator 设置全局结构体验证器（如 validate tag 校验）。
func SetDefaultValidator(v Validator) {
	defaultValidator = v
}
