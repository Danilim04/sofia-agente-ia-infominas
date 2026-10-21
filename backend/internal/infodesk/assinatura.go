package infodesk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Headers e formato da assinatura dos webhooks (docs/infodesk/05-webhooks.md,
// seção 5.4).
const (
	HeaderEvento     = "X-Infominas-Event"
	HeaderEntrega    = "X-Infominas-Delivery" // id da ENTREGA (muda a cada reentrega) — só log
	HeaderTimestamp  = "X-Infominas-Timestamp"
	HeaderAssinatura = "X-Infominas-Signature"

	prefixoAssinatura = "sha256="

	// JanelaTimestampPadrao é a tolerância de relógio do PDF v1: timestamps a
	// mais de 5 minutos de "agora" (para o passado OU para o futuro) são
	// recusados como possível replay.
	JanelaTimestampPadrao = 5 * time.Minute
)

// Erros de Verificador.Verificar. O receptor responde 401 para os três; a
// distinção existe para o alerta `webhook_timestamp_fora_da_janela`
// (docs/plano/01 seção B), que só deve disparar para uma entrega AUTÊNTICA
// fora da janela (ErrTimestampForaDaJanela).
var (
	ErrAssinaturaInvalida    = errors.New("infodesk: assinatura do webhook inválida")
	ErrTimestampInvalido     = errors.New("infodesk: X-Infominas-Timestamp ausente ou ilegível")
	ErrTimestampForaDaJanela = errors.New("infodesk: timestamp do webhook fora da janela")
)

// Verificador confere a assinatura de uma entrega de webhook:
//
//	X-Infominas-Signature = "sha256=" + hex(HMAC-SHA256(secret, "<timestamp>.<corpo bruto>"))
//
// onde <timestamp> é o valor de X-Infominas-Timestamp (segundos Unix) e o
// corpo é o BRUTO, os bytes exatos recebidos, antes de qualquer parse. Além
// da assinatura, recusa timestamps com |now − ts| > Janela (replay).
type Verificador struct {
	Secret string           // whsec_… — segredo do webhook (≠ chave de API)
	Janela time.Duration    // 0 = JanelaTimestampPadrao (5 min); WEBHOOK_JANELA na E2.1
	Now    func() time.Time // 0 = time.Now; injetável nos testes
}

// Verificar devolve nil quando a entrega é autêntica e recente. Ordem das
// checagens: timestamp legível → assinatura (tempo constante) → janela. A
// janela vem por último de propósito: só uma entrega com assinatura válida
// pode causar ErrTimestampForaDaJanela. Segredo ou header vazio nunca passa.
func (v Verificador) Verificar(timestamp string, raw []byte, header string) error {
	ts := strings.TrimSpace(timestamp)
	seg, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || seg <= 0 {
		return ErrTimestampInvalido
	}
	if !assinaturaConfere(v.Secret, ts, raw, header) {
		return ErrAssinaturaInvalida
	}
	agora := time.Now
	if v.Now != nil {
		agora = v.Now
	}
	janela := v.Janela
	if janela <= 0 {
		janela = JanelaTimestampPadrao
	}
	dif := agora().Sub(time.Unix(seg, 0))
	if dif < 0 {
		dif = -dif
	}
	if dif > janela {
		return ErrTimestampForaDaJanela
	}
	return nil
}

// VerificarAssinatura é o atalho de Verificador{Secret: secret}.Verificar com
// relógio real e janela de 5 min. Quem precisa de Now injetável ou de janela
// configurável (o receptor de webhook, os testes) usa Verificador.
func VerificarAssinatura(secret, timestamp string, raw []byte, header string) bool {
	return Verificador{Secret: secret}.Verificar(timestamp, raw, header) == nil
}

// Assinar produz o valor do header X-Infominas-Signature para o timestamp e o
// corpo dados ("sha256=<hex minúsculo>"). Serve ao mock do Infodesk e aos
// testes; o gateway só verifica.
func Assinar(secret, timestamp string, raw []byte) string {
	return prefixoAssinatura + hex.EncodeToString(assinar(secret, strings.TrimSpace(timestamp), raw))
}

// assinaturaConfere compara o header com o HMAC esperado em tempo constante;
// hex em maiúsculas ou minúsculas; segredo ou header vazio = falso.
func assinaturaConfere(secret, ts string, raw []byte, header string) bool {
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
	return hmac.Equal(assinar(secret, ts, raw), recebida)
}

func assinar(secret, ts string, raw []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte{'.'})
	mac.Write(raw)
	return mac.Sum(nil)
}
