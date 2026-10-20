// Package arquitetura contém só testes: regras de dependência entre os pacotes
// do gateway (camada 1 da esteira — docs/plano/04-esteira-qualidade.md).
//
// A fonte das regras é a seção "Camada 1" do plano. Uma regra sobre pacote que
// ainda não existe é vacuamente verdadeira (vale a partir do dia em que ele
// nasce). A regra de "legado" (ninguém importa os pacotes do domínio antigo)
// só é cobrada depois que backend/ sai de docs/plano/legado-permitido.txt (E1.1).
//
// Os nomes dos pacotes antigos aparecem abaixo em pedaços concatenados de
// propósito: o teste de arquitetura Python (tests/test_arquitetura.py) varre
// este arquivo procurando exatamente esses termos.
package arquitetura_test

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// grafo devolve importPath → imports diretos de todos os pacotes do módulo.
func grafo(t *testing.T) (modulo string, deps map[string][]string) {
	t.Helper()
	raiz := raizDoModulo(t)
	out, err := exec.CommandContext(context.Background(), "go", "list", "-f", "{{.ImportPath}}|{{join .Imports \" \"}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	deps = map[string][]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		partes := strings.SplitN(sc.Text(), "|", 2)
		if len(partes) != 2 {
			continue
		}
		deps[partes[0]] = strings.Fields(partes[1])
	}
	mod, err := os.ReadFile(filepath.Join(raiz, "go.mod"))
	if err != nil {
		t.Fatalf("go.mod: %v", err)
	}
	for _, l := range strings.Split(string(mod), "\n") {
		if strings.HasPrefix(l, "module ") {
			modulo = strings.TrimSpace(strings.TrimPrefix(l, "module "))
		}
	}
	return modulo, deps
}

func raizDoModulo(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// regra: pacote (sufixo após o módulo) → prefixos de import proibidos.
type regra struct {
	pacote    string
	proibidos []string
	motivo    string
}

func TestArquitetura_DependenciasEntreCamadas(t *testing.T) {
	modulo, deps := grafo(t)
	regras := []regra{
		{"/internal/infodesk", []string{"/internal/engine", "/internal/store", "/internal/toolsapi", "/internal/webhook"},
			"o cliente da API não conhece quem o usa"},
		{"/internal/engine", []string{"/internal/toolsapi"}, "a engine não depende da API de tools"},
		{"/internal/toolsapi", []string{"/internal/webhook"}, "a API de tools não depende do receptor de webhook"},
		{"/internal/store", []string{"/internal/"}, "o store não importa nada de internal/ (é folha)"},
		{"/internal/alerta", []string{"/internal/engine", "/internal/toolsapi", "/internal/webhook"}, "alerta é folha"},
	}
	for _, r := range regras {
		pkg := modulo + r.pacote
		imps, existe := deps[pkg]
		if !existe {
			continue // pacote ainda não existe: regra vale a partir do nascimento
		}
		for _, imp := range imps {
			for _, p := range r.proibidos {
				if strings.HasPrefix(imp, modulo+p) && imp != pkg {
					t.Errorf("%s importa %s — proibido (%s)", r.pacote, strings.TrimPrefix(imp, modulo), r.motivo)
				}
			}
		}
	}
}

func TestArquitetura_NinguemImportaLegado(t *testing.T) {
	if legadoPermitido(t, "backend/") {
		t.Skip("backend/ ainda está em docs/plano/legado-permitido.txt (sai em E1.1)")
	}
	modulo, deps := grafo(t)
	legados := []string{"/internal/chat" + "woot", "/internal/mon" + "go", "/internal/s" + "ac", "/internal/identity", "go.mon" + "godb.org/"}
	for pkg, imps := range deps {
		for _, imp := range imps {
			for _, l := range legados {
				if strings.HasPrefix(imp, modulo+l) || strings.HasPrefix(imp, l) {
					t.Errorf("%s importa %s (domínio antigo — ADR 0003)", strings.TrimPrefix(pkg, modulo), imp)
				}
			}
		}
	}
	for pkg := range deps {
		for _, l := range legados[:4] {
			if strings.HasPrefix(pkg, modulo+l) {
				t.Errorf("pacote legado ainda existe: %s", strings.TrimPrefix(pkg, modulo))
			}
		}
	}
}

func legadoPermitido(t *testing.T, prefixo string) bool {
	t.Helper()
	raiz := raizDoModulo(t)
	b, err := os.ReadFile(filepath.Join(raiz, "..", "docs", "plano", "legado-permitido.txt"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if l == prefixo || strings.HasPrefix(prefixo, l) {
			return true
		}
	}
	return false
}
