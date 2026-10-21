package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bot-infominas/internal/infodesk"
)

func meOK() infodesk.Me {
	return infodesk.Me{
		OrganizationID: "org-1", KeyID: "k1", Name: "Bot de atendimento", HandlesConversations: true,
		Scopes: []string{"tickets:read", "tickets:write", "conversations:read", "conversations:write", "contacts:read", "events:read", "whatsapp:send"},
	}
}

func TestValidarChaveOK(t *testing.T) {
	if err := validarChave(meOK(), ""); err != nil {
		t.Fatalf("chave boa: %v", err)
	}
	if err := validarChave(meOK(), "org-1"); err != nil {
		t.Fatalf("organização configurada e igual: %v", err)
	}
}

func TestValidarChaveRecusa(t *testing.T) {
	semConduzir := meOK()
	semConduzir.HandlesConversations = false
	semEscopo := meOK()
	semEscopo.Scopes = []string{"tickets:read", "conversations:read"}
	semOrg := meOK()
	semOrg.OrganizationID = ""
	casos := map[string]struct {
		me   infodesk.Me
		org  string
		cita string
	}{
		"não conduz o atendimento": {semConduzir, "", "handlesConversations=false"},
		"escopo faltando":          {semEscopo, "", "tickets:write"},
		"sem organizationId":       {semOrg, "", "organizationId"},
		"organização diferente":    {meOK(), "outra", "INFODESK_ORGANIZATION_ID"},
	}
	for nome, c := range casos {
		err := validarChave(c.me, c.org)
		if !errors.Is(err, ErrChaveInvalida) || !strings.Contains(err.Error(), c.cita) {
			t.Errorf("%s: esperava ErrChaveInvalida citando %q, veio %v", nome, c.cita, err)
		}
	}
}

type meFalso struct {
	respostas []error
	chamadas  int
}

func (m *meFalso) Me(context.Context) (infodesk.Me, error) {
	i := m.chamadas
	m.chamadas++
	if i < len(m.respostas) && m.respostas[i] != nil {
		return infodesk.Me{}, m.respostas[i]
	}
	return meOK(), nil
}

// GET /me no boot repete em falha transitória e desiste na definitiva (401).
func TestConsultarMeRepeteSoEmTransitorio(t *testing.T) {
	var esperas []time.Duration
	dormir := func(d time.Duration) { esperas = append(esperas, d) }

	m := &meFalso{respostas: []error{&infodesk.HTTPError{Status: 503}, &infodesk.HTTPError{Status: 503}}}
	me, err := consultarMe(context.Background(), m, dormir)
	if err != nil || me.OrganizationID != "org-1" || m.chamadas != 3 || len(esperas) != 2 {
		t.Fatalf("2 falhas e sucesso: me=%+v err=%v chamadas=%d esperas=%v", me, err, m.chamadas, esperas)
	}

	m = &meFalso{respostas: []error{&infodesk.HTTPError{Status: 401, Code: infodesk.CodeUnauthorized}}}
	if _, err := consultarMe(context.Background(), m, dormir); err == nil || m.chamadas != 1 {
		t.Fatalf("401 não repete: err=%v chamadas=%d", err, m.chamadas)
	}
}
