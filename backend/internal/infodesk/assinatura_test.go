package infodesk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

const (
	segredoTeste = "whsec_teste_123"
	corpoTeste   = `{"id":"ev-1","event":"message.received","data":{"conversationId":"c1"}}`
)

// hmacHex calcula o vetor de referência de forma independente do pacote.
func hmacHex(secret string, corpo []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(corpo)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerificarAssinatura(t *testing.T) {
	raw := []byte(corpoTeste)
	valido := hmacHex(segredoTeste, raw)

	casos := map[string]struct {
		secret string
		raw    []byte
		header string
		quer   bool
	}{
		"valida":                  {segredoTeste, raw, "sha256=" + valido, true},
		"hexMaiusculo":            {segredoTeste, raw, "sha256=" + strings.ToUpper(valido), true},
		"prefixoMaiusculo":        {segredoTeste, raw, "SHA256=" + valido, true},
		"comEspacosEmVolta":       {segredoTeste, raw, "  sha256=" + valido + " \n", true},
		"headerVazio":             {segredoTeste, raw, "", false},
		"soPrefixo":               {segredoTeste, raw, "sha256=", false},
		"semPrefixo":              {segredoTeste, raw, valido, false},
		"prefixoErrado":           {segredoTeste, raw, "sha1=" + valido, false},
		"hexInvalido":             {segredoTeste, raw, "sha256=zz" + valido[2:], false},
		"tamanhoErrado":           {segredoTeste, raw, "sha256=" + valido[:40], false},
		"segredoErrado":           {"outro", raw, "sha256=" + valido, false},
		"segredoVazio":            {"", raw, "sha256=" + hmacHex("", raw), false},
		"corpoAlterado":           {segredoTeste, []byte(corpoTeste + " "), "sha256=" + valido, false},
		"corpoVazioAssinadoVazio": {segredoTeste, nil, "sha256=" + hmacHex(segredoTeste, nil), true},
	}
	for nome, cs := range casos {
		t.Run(nome, func(t *testing.T) {
			if got := VerificarAssinatura(cs.secret, cs.raw, cs.header); got != cs.quer {
				t.Errorf("VerificarAssinatura = %v, quer %v", got, cs.quer)
			}
		})
	}
}

func TestAssinar_RoundTrip(t *testing.T) {
	raw := []byte(corpoTeste)
	h := Assinar(segredoTeste, raw)
	if h != "sha256="+hmacHex(segredoTeste, raw) {
		t.Errorf("Assinar = %q", h)
	}
	if !VerificarAssinatura(segredoTeste, raw, h) {
		t.Error("o que Assinar produz deve verificar")
	}
	if VerificarAssinatura("outro", raw, h) {
		t.Error("segredo diferente não pode verificar")
	}
}

func TestVerificarAssinatura_VetorConhecido(t *testing.T) {
	// HMAC-SHA256(key="key", msg="The quick brown fox jumps over the lazy dog") — vetor público (RFC/Wikipedia).
	const vetor = "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"
	msg := []byte("The quick brown fox jumps over the lazy dog")
	if !VerificarAssinatura("key", msg, "sha256="+vetor) {
		t.Error("vetor conhecido deveria verificar")
	}
	if VerificarAssinatura("key", []byte("The quick brown fox jumps over the lazy cat"), "sha256="+vetor) {
		t.Error("mensagem diferente não pode verificar")
	}
}
