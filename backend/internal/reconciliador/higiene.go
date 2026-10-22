package reconciliador

import (
	"context"
	"fmt"
	"time"
)

// retencaoPropostaCadastro é por quanto tempo uma proposta de cadastro não
// executada fica no store. A proposta vale 15 min (toolsapi.propostaCadastroValidade,
// o mesmo prazo do cérebro); a margem cobre o relógio e um "sim" no limite. Depois
// disso ela só serviria para guardar nome e e-mail do cliente em claro.
const retencaoPropostaCadastro = 20 * time.Minute

// cadastrosVencidos apaga as propostas de cadastro vencidas (E4.5, I2): nunca
// foram ao Infodesk e carregam PII em claro (nome, e-mail). Também limpa nome e
// e-mail de linhas `feito` antigas. Falha aqui não derruba o ciclo.
func (r *Reconciliador) cadastrosVencidos(ctx context.Context) error {
	n, err := r.st.ExpirarCadastros(ctx, r.opt.Now().Add(-retencaoPropostaCadastro))
	if err != nil {
		return fmt.Errorf("expirar cadastros: %w", err)
	}
	if n > 0 {
		r.log.Info("propostas de cadastro vencidas apagadas", "propostas", n)
	}
	return nil
}
