package vm

import (
	"encoding/hex"
	"errors"

	"github.com/ethereum/go-ethereum/log"
)

var (
	ErrSetAccessRights = errors.New("call to b8ff1dba : setAccessRights()")
)

// Forbid interdicts unwanted functions in current contracts
func Forbid(data []byte) error {
	if len(data) == 4 && hex.EncodeToString(data) == "b8ff1dba" {
		log.Warn("interdicting", "setAccessRights()", "b8ff1dba")
		return ErrSetAccessRights
	}
	return nil
}
