package engine

import (
	"context"
	"errors"
	"strconv"

	"bot-infominas/internal/infodesk"
)

// DescreverErro resume um erro do Infodesk para LOG sem a mensagem do servidor
// (que pode ecoar dado do cliente): só o tipo, o status HTTP, o `code` e a rota.
// É o que vai nos campos "err" dos logs em vez do erro cru (E4.5, M1). Erro que
// não é do cliente do Infodesk vira "erro interno".
func DescreverErro(err error) string {
	var he *infodesk.HTTPError
	var re *infodesk.ErroRede
	var rp *infodesk.ErroResposta
	switch {
	case errors.As(err, &he):
		return "http " + strconv.Itoa(he.Status) + " " + he.Code + " " + he.Rota
	case errors.As(err, &re):
		return "rede " + re.Rota
	case errors.As(err, &rp):
		return "resposta inválida " + rp.Rota
	case errors.Is(err, infodesk.ErrTicketSemNumero):
		return "ticket sem número na resposta"
	case errors.Is(err, context.DeadlineExceeded):
		return "prazo esgotado"
	default:
		return "erro interno"
	}
}

// ErroDoInfodesk diz se o erro veio do cliente do Infodesk (e portanto pode
// carregar texto do servidor): HTTP, rede ou resposta ilegível.
func ErroDoInfodesk(err error) bool {
	var he *infodesk.HTTPError
	var re *infodesk.ErroRede
	var rp *infodesk.ErroResposta
	return errors.As(err, &he) || errors.As(err, &re) || errors.As(err, &rp)
}

// erroParaLog é o valor do campo "err" dos logs da engine: erro do Infodesk é
// resumido (DescreverErro, sem a mensagem do servidor); erro interno (store,
// contexto cancelado…) segue inteiro, para não perder o diagnóstico.
func erroParaLog(err error) string {
	if ErroDoInfodesk(err) {
		return DescreverErro(err)
	}
	return err.Error()
}
