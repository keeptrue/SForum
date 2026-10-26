package identity

import (
	"crypto/rand"
	"errors"
	"math/big"
)

// errPasswordGeneration 只会在密码策略本身不可满足时出现（例如策略要求的字符类
// 数量超过最小长度）；调用方按 500 处理，不向客户端暴露细节。
var errPasswordGeneration = errors.New("identity: cannot generate a policy-compliant password")

const (
	generatedPasswordMinLength = 16
	passwordLowerAlphabet      = "abcdefghijkmnopqrstuvwxyz"
	passwordUpperAlphabet      = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	passwordNumberAlphabet     = "23456789"
	passwordSymbolAlphabet     = "!@#$%^&*()-_=+"
)

// GenerateCompliantPassword 生成满足站点密码策略的随机初始密码。
// 先按策略要求的字符类各取一个字符，再用混合字母表补足长度并洗牌，
// 最后用 policy.Validate 复核；重试上限只是防御性兜底。
func GenerateCompliantPassword(policy PasswordPolicy) (string, error) {
	policy = policy.Normalized()
	length := policy.MinLength
	if length < generatedPasswordMinLength {
		length = generatedPasswordMinLength
	}
	if length > policy.MaxLength {
		length = policy.MaxLength
	}
	if length <= 0 {
		return "", errPasswordGeneration
	}

	required := make([]string, 0, 4)
	if policy.RequireLowercase {
		required = append(required, passwordLowerAlphabet)
	}
	if policy.RequireUppercase {
		required = append(required, passwordUpperAlphabet)
	}
	if policy.RequireNumber {
		required = append(required, passwordNumberAlphabet)
	}
	if policy.RequireSymbol {
		required = append(required, passwordSymbolAlphabet)
	}
	if len(required) > length {
		return "", errPasswordGeneration
	}

	alphabet := passwordLowerAlphabet + passwordUpperAlphabet + passwordNumberAlphabet + passwordSymbolAlphabet
	for attempt := 0; attempt < 8; attempt++ {
		chars := make([]byte, 0, length)
		for _, set := range required {
			value, err := randomPasswordChar(set)
			if err != nil {
				return "", err
			}
			chars = append(chars, value)
		}
		for len(chars) < length {
			value, err := randomPasswordChar(alphabet)
			if err != nil {
				return "", err
			}
			chars = append(chars, value)
		}
		if err := shufflePasswordChars(chars); err != nil {
			return "", err
		}
		password := string(chars)
		if len(policy.Validate(password)) == 0 {
			return password, nil
		}
	}
	return "", errPasswordGeneration
}

func randomPasswordChar(alphabet string) (byte, error) {
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
	if err != nil {
		return 0, err
	}
	return alphabet[index.Int64()], nil
}

func shufflePasswordChars(values []byte) error {
	for i := len(values) - 1; i > 0; i-- {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return err
		}
		j := int(index.Int64())
		values[i], values[j] = values[j], values[i]
	}
	return nil
}
