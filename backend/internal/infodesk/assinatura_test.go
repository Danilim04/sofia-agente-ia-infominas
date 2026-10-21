package infodesk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	segredoTeste = "whsec_teste_123"
	corpoTeste   = `{"id":"evt_000000000001","seq":1,"event":"message.received","data":{}}`
)

// instanteTeste é o "agora" fixo dos testes de assinatura.
var instanteTeste = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func tsTeste(d time.Duration) string {
	return strconv.FormatInt(instanteTeste.Add(d).Unix(), 10)
}

// hmacHex calcula o vetor de referência de forma independente do pacote:
// HMAC-SHA256(secret, "<timestamp>.<corpo>").
func hmacHex(secret, ts string, corpo []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(corpo)
	return hex.EncodeToString(mac.Sum(nil))
}

func verificadorTeste() Verificador {
	return Verificador{Secret: segredoTeste, Now: func() time.Time { return instanteTeste }}
}

func TestVerificarAssinatura(t *testing.T) {
	raw := []byte(corpoTeste)
	ts := tsTeste(0)
	valido := hmacHex(segredoTeste, ts, raw)

	casos := map[string]struct {
		secret string
		ts     string
		raw    []byte
		header string
		quer   bool
	}{
		"valida":                  {segredoTeste, ts, raw, "sha256=" + valido, true},
		"hexMaiusculo":            {segredoTeste, ts, raw, "sha256=" + strings.ToUpper(valido), true},
		"prefixoMaiusculo":        {segredoTeste, ts, raw, "SHA256=" + valido, true},
		"comEspacosEmVolta":       {segredoTeste, ts, raw, "  sha256=" + valido + " \n", true},
		"timestampComEspaco":      {segredoTeste, " " + ts + " ", raw, "sha256=" + valido, true},
		"headerVazio":             {segredoTeste, ts, raw, "", false},
		"soPrefixo":               {segredoTeste, ts, raw, "sha256=", false},
		"semPrefixo":              {segredoTeste, ts, raw, valido, false},
		"prefixoErrado":           {segredoTeste, ts, raw, "sha1=" + valido, false},
		"hexInvalido":             {segredoTeste, ts, raw, "sha256=zz" + valido[2:], false},
		"tamanhoErrado":           {segredoTeste, ts, raw, "sha256=" + valido[:40], false},
		"segredoErrado":           {"outro", ts, raw, "sha256=" + valido, false},
		"segredoVazio":            {"", ts, raw, "sha256=" + hmacHex("", ts, raw), false},
		"corpoAlterado":           {segredoTeste, ts, []byte(corpoTeste + " "), "sha256=" + valido, false},
		"corpoVazioAssinadoVazio": {segredoTeste, ts, nil, "sha256=" + hmacHex(segredoTeste, ts, nil), true},
		// A assinatura antiga (HMAC só do corpo, sem timestamp) NÃO vale mais.
		"assinaturaAntigaSemTimestamp": {segredoTeste, ts, raw, "sha256=" + hmacHexSemTimestamp(segredoTeste, raw), false},
		// Trocar o timestamp invalida a assinatura (ele é assinado junto).
		"timestampTrocado": {segredoTeste, tsTeste(time.Second), raw, "sha256=" + valido, false},
	}
	for nome, cs := range casos {
		t.Run(nome, func(t *testing.T) {
			v := verificadorTeste()
			v.Secret = cs.secret
			err := v.Verificar(cs.ts, cs.raw, cs.header)
			if got := err == nil; got != cs.quer {
				t.Errorf("Verificar err = %v, quer ok=%v", err, cs.quer)
			}
		})
	}
}

func hmacHexSemTimestamp(secret string, corpo []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(corpo)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerificar_Janela(t *testing.T) {
	raw := []byte(corpoTeste)
	cinco := 5 * time.Minute
	casos := map[string]struct {
		desvio time.Duration
		quer   error
	}{
		"agora":               {0, nil},
		"passadoDentro":       {-4*time.Minute - 59*time.Second, nil},
		"passadoNoLimite":     {-cinco, nil}, // |now − ts| > 5 min recusa; == 5 min passa
		"passadoExpirado":     {-cinco - time.Second, ErrTimestampForaDaJanela},
		"passadoMuitoAntigo":  {-24 * time.Hour, ErrTimestampForaDaJanela},
		"futuroDentro":        {4*time.Minute + 59*time.Second, nil},
		"futuroNoLimite":      {cinco, nil},
		"futuroAlemDaJanela":  {cinco + time.Second, ErrTimestampForaDaJanela},
		"futuroMuitoDistante": {24 * time.Hour, ErrTimestampForaDaJanela},
	}
	for nome, cs := range casos {
		t.Run(nome, func(t *testing.T) {
			ts := tsTeste(cs.desvio)
			h := Assinar(segredoTeste, ts, raw)
			err := verificadorTeste().Verificar(ts, raw, h)
			if !errors.Is(err, cs.quer) {
				t.Errorf("err = %v, quer %v", err, cs.quer)
			}
		})
	}

	t.Run("janelaConfiguravel", func(t *testing.T) {
		ts := tsTeste(-8 * time.Minute)
		h := Assinar(segredoTeste, ts, raw)
		v := verificadorTeste()
		v.Janela = 10 * time.Minute
		if err := v.Verificar(ts, raw, h); err != nil {
			t.Errorf("janela de 10 min deveria aceitar 8 min: %v", err)
		}
		v.Janela = time.Minute
		if err := v.Verificar(ts, raw, h); !errors.Is(err, ErrTimestampForaDaJanela) {
			t.Errorf("janela de 1 min deveria recusar 8 min: %v", err)
		}
	})
	t.Run("relogioInjetadoMuda", func(t *testing.T) {
		ts := tsTeste(0)
		h := Assinar(segredoTeste, ts, raw)
		v := verificadorTeste()
		v.Now = func() time.Time { return instanteTeste.Add(time.Hour) }
		if err := v.Verificar(ts, raw, h); !errors.Is(err, ErrTimestampForaDaJanela) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestVerificar_ClassificaOErro(t *testing.T) {
	raw := []byte(corpoTeste)
	ts := tsTeste(0)
	h := Assinar(segredoTeste, ts, raw)

	for nome, cs := range map[string]struct {
		ts     string
		header string
		quer   error
	}{
		"timestampAusente":  {"", h, ErrTimestampInvalido},
		"timestampTexto":    {"ontem", h, ErrTimestampInvalido},
		"timestampNegativo": {"-5", h, ErrTimestampInvalido},
		"timestampZero":     {"0", h, ErrTimestampInvalido},
		"timestampDecimal":  {ts + ".5", h, ErrTimestampInvalido},
		"assinaturaRuim":    {ts, "sha256=" + strings.Repeat("0", 64), ErrAssinaturaInvalida},
		"assinaturaVazia":   {ts, "", ErrAssinaturaInvalida},
	} {
		t.Run(nome, func(t *testing.T) {
			if err := verificadorTeste().Verificar(cs.ts, raw, cs.header); !errors.Is(err, cs.quer) {
				t.Errorf("err = %v, quer %v", err, cs.quer)
			}
		})
	}

	// Timestamp fora da janela com assinatura INVÁLIDA é assinatura inválida:
	// só entrega autêntica dispara o alerta de janela.
	velho := tsTeste(-time.Hour)
	if err := verificadorTeste().Verificar(velho, raw, "sha256="+strings.Repeat("0", 64)); !errors.Is(err, ErrAssinaturaInvalida) {
		t.Errorf("err = %v, quer ErrAssinaturaInvalida", err)
	}
	// Sem segredo nunca passa, nem com assinatura "calculada" com segredo vazio.
	semSegredo := Verificador{Now: func() time.Time { return instanteTeste }}
	if err := semSegredo.Verificar(ts, raw, "sha256="+hmacHex("", ts, raw)); !errors.Is(err, ErrAssinaturaInvalida) {
		t.Errorf("err = %v", err)
	}
}

func TestVerificarAssinatura_AtalhoComRelogioReal(t *testing.T) {
	raw := []byte(corpoTeste)
	agora := strconv.FormatInt(time.Now().Unix(), 10)
	if !VerificarAssinatura(segredoTeste, agora, raw, Assinar(segredoTeste, agora, raw)) {
		t.Error("assinatura de agora deveria verificar")
	}
	velho := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	if VerificarAssinatura(segredoTeste, velho, raw, Assinar(segredoTeste, velho, raw)) {
		t.Error("timestamp de 10 min atrás deveria ser recusado")
	}
	futuro := strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)
	if VerificarAssinatura(segredoTeste, futuro, raw, Assinar(segredoTeste, futuro, raw)) {
		t.Error("timestamp 10 min no futuro deveria ser recusado")
	}
}

func TestAssinar_RoundTrip(t *testing.T) {
	raw := []byte(corpoTeste)
	ts := tsTeste(0)
	h := Assinar(segredoTeste, ts, raw)
	if h != "sha256="+hmacHex(segredoTeste, ts, raw) {
		t.Errorf("Assinar = %q", h)
	}
	if err := verificadorTeste().Verificar(ts, raw, h); err != nil {
		t.Errorf("o que Assinar produz deve verificar: %v", err)
	}
	outro := verificadorTeste()
	outro.Secret = "outro"
	if err := outro.Verificar(ts, raw, h); err == nil {
		t.Error("segredo diferente não pode verificar")
	}
}

func TestVerificar_VetorConhecido(t *testing.T) {
	// Vetor fixo, calculado FORA do Go (openssl dgst -sha256 -hmac) sobre
	// "<timestamp>.<corpo>" — protege contra errar o formato da mensagem
	// assinada nos dois lados (pacote e teste).
	const (
		secret = "whsec_vetor"
		ts     = "1760875200"
		corpo  = `{"id":"evt_000000000123","seq":123,"event":"ping"}`
		vetor  = "4a47fc551425290c994229ef78e0a6468f9b318dcd592f9e46abbe9236425fbb"
	)
	agora := time.Unix(1760875200+30, 0)
	v := Verificador{Secret: secret, Now: func() time.Time { return agora }}
	if err := v.Verificar(ts, []byte(corpo), "sha256="+vetor); err != nil {
		t.Errorf("vetor conhecido deveria verificar: %v", err)
	}
	if got := Assinar(secret, ts, []byte(corpo)); got != "sha256="+vetor {
		t.Errorf("Assinar = %q", got)
	}
	if err := v.Verificar(ts, []byte(corpo+" "), "sha256="+vetor); !errors.Is(err, ErrAssinaturaInvalida) {
		t.Errorf("corpo diferente não pode verificar: %v", err)
	}
	if err := v.Verificar("1760875201", []byte(corpo), "sha256="+vetor); !errors.Is(err, ErrAssinaturaInvalida) {
		t.Errorf("timestamp diferente não pode verificar: %v", err)
	}
}
