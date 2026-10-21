package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"bot-infominas/internal/infodesk"
)

// escoposMinimos são os escopos que a chave da Sofia precisa na v1
// (docs/infodesk/01; docs/plano/01, seção B "Validar chave no boot").
var escoposMinimos = []string{
	"tickets:read", "tickets:write",
	"conversations:read", "conversations:write",
	"contacts:read", "events:read",
}

// ErrChaveInvalida: a chave de API não serve para a Sofia (boot falha).
var ErrChaveInvalida = errors.New("chave do Infodesk inválida para a Sofia")

// validarChave confere o resultado de GET /me: a chave precisa conduzir
// conversas (modo bot externo, "Conduz o atendimento") e ter os escopos
// mínimos; se INFODESK_ORGANIZATION_ID foi configurado, tem de bater com a
// organização da chave. A mensagem lista o que falta, sem segredo.
func validarChave(me infodesk.Me, organizacaoConfigurada string) error {
	var problemas []string
	if !me.HandlesConversations {
		problemas = append(problemas, `a chave não está em modo "Conduz o atendimento" (handlesConversations=false)`)
	}
	var faltam []string
	for _, e := range escoposMinimos {
		if !slices.Contains(me.Scopes, e) {
			faltam = append(faltam, e)
		}
	}
	if len(faltam) > 0 {
		problemas = append(problemas, "faltam escopos: "+strings.Join(faltam, ", "))
	}
	if me.OrganizationID == "" {
		problemas = append(problemas, "GET /me não devolveu organizationId")
	} else if organizacaoConfigurada != "" && organizacaoConfigurada != me.OrganizationID {
		problemas = append(problemas, "INFODESK_ORGANIZATION_ID não é a organização da chave")
	}
	if len(problemas) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrChaveInvalida, strings.Join(problemas, "; "))
}

// consultarMe faz GET /me no boot com poucas novas tentativas em falha
// transitória (Infodesk subindo junto, rede). Erro definitivo (401/403) sai
// na hora.
func consultarMe(ctx context.Context, c interface {
	Me(ctx context.Context) (infodesk.Me, error)
}, dormir func(time.Duration)) (infodesk.Me, error) {
	var err error
	for tentativa := 0; tentativa < 4; tentativa++ {
		var me infodesk.Me
		if me, err = c.Me(ctx); err == nil {
			return me, nil
		}
		if !infodesk.Transitorio(err) || ctx.Err() != nil {
			break
		}
		dormir(time.Duration(tentativa+1) * 2 * time.Second)
	}
	return infodesk.Me{}, fmt.Errorf("GET /me: %w", err)
}
