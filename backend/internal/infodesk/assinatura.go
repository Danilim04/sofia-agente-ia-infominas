package infodesk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Headers e formato da assinatura dos webhooks (05-webhooks.md).
const (
	HeaderEvento      = "X-Infominas-Event"
	HeaderAssinatura  = "X-Infominas-Signature"
	prefixoAssinatura = "sha256="
)

// VerificarAssinatura confere o header X-Infominas-Signature
// ("sha256=<hex>") contra o HMAC-SHA256 do corpo BRUTO (os bytes exatos
// recebidos, antes de qualquer parse) com o webhookSecret. Comparação em tempo
// constante; hex em maiúsculas ou minúsculas; segredo ou header vazio = falso.
//
// Não há timestamp assinado (sem proteção de replay na API): o receptor deve
// deduplicar por `id` da entrega e descartar `createdAt` antigo.
func VerificarAssinatura(secret string, raw []byte, header string) bool {
	if secret == "" {
		return false
	}
	h := strings.TrimSpace(header)
	if len(h) <= len(prefixoAssinatura) || !strings.EqualFold(h[:len(prefixoAssinatura)], prefixoAssinatura) {
		return false
	}
	recebida, err := hex.DecodeString(h[len(prefixoAssinatura):])
	if err != nil || len(recebida) != sha256.Size {
		return false
	}
	return hmac.Equal(assinar(secret, raw), recebida)
}

// Assinar produz o valor do header no formato do Infodesk ("sha256=<hex
// minúsculo>"). Serve ao mock do Infodesk e aos testes; o gateway só verifica.
func Assinar(secret string, raw []byte) string {
	return prefixoAssinatura + hex.EncodeToString(assinar(secret, raw))
}

func assinar(secret string, raw []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	return mac.Sum(nil)
}
