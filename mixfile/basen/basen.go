package basen

import (
	"math/big"
)

const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

var base = big.NewInt(int64(len(alphabet)))

// 编码
func Encode(data []byte) string {
	x := new(big.Int).SetBytes(data)

	if x.Cmp(big.NewInt(0)) == 0 {
		return string(alphabet[0])
	}

	result := make([]byte, 0)

	mod := new(big.Int)

	for x.Cmp(big.NewInt(0)) > 0 {
		x.DivMod(x, base, mod)
		result = append(result, alphabet[mod.Int64()])
	}

	// 反转
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return string(result)
}

// 解码
func Decode(s string) []byte {
	x := big.NewInt(0)

	for _, ch := range s {
		index := int64(-1)
		for i, c := range alphabet {
			if c == ch {
				index = int64(i)
				break
			}
		}
		if index < 0 {
			panic("invalid character")
		}

		x.Mul(x, base)
		x.Add(x, big.NewInt(index))
	}

	return x.Bytes()
}
