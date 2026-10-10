package containerapp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
)

// seal 使用独立派生密钥加密环境变量与仓库密码，并绑定应用编号。
func seal(secret, id string, spec Spec) (string, error) {
	key := sha256.Sum256([]byte("stacknest-app-config-v1\x00" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	box, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, box.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(box.Seal(nonce, nonce, raw, []byte(id))), nil
}

// open 解密且再次验证配置；密文不可移至另一个应用。
func open(secret, id, value string) (Spec, error) {
	var spec Spec
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return spec, errors.New("应用配置解密失败")
	}
	key := sha256.Sum256([]byte("stacknest-app-config-v1\x00" + secret))
	block, _ := aes.NewCipher(key[:])
	box, _ := cipher.NewGCM(block)
	if len(raw) < box.NonceSize() {
		return spec, errors.New("应用配置解密失败")
	}
	plain, err := box.Open(nil, raw[:box.NonceSize()], raw[box.NonceSize():], []byte(id))
	if err != nil {
		return spec, errors.New("应用配置解密失败")
	}
	if err = json.Unmarshal(plain, &spec); err != nil {
		return spec, errors.New("应用配置格式无效")
	}
	return spec, spec.Validate()
}
