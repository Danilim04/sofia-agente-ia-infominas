package mongo

import (
	"encoding/json"
	"testing"
)

// sampleDoc reproduz a forma do documento real do Mongo (subconjunto): um grupo
// INATIVO (AZAPFY) e um grupo ATIVO (AZAPERS) com módulos ativos e inativos.
const sampleDoc = `{
  "login": "10596693664",
  "nome": "Daniel Ferraz",
  "email": "daniel.ferraz@azapfy.com.br",
  "grupos": {
    "AZAPFY": {
      "ativo": false,
      "grupo_user": "COLABORADOR",
      "area": "SAC",
      "bases": {
        "CASA CHEIA": {"nome": "CASA CHEIA", "sigla": "CSC",
          "modulos": {"pesquisa": {"ativo": true, "web": true}}}
      }
    },
    "AZAPERS": {
      "ativo": true,
      "grupo_user": "COLABORADOR",
      "area": "SAC",
      "bases": {
        "MATRIZ": {
          "nome": "MATRIZ",
          "sigla": "MAT",
          "modulos": {
            "pesquisa":            {"ativo": true,  "web": true},
            "rastreamento":        {"ativo": true,  "web": true},
            "ocorrencia":          {"ativo": true,  "web": true},
            "romaneio_automatico": {"ativo": false, "web": false},
            "torre_controle":      {"ativo": false, "web": false}
          }
        }
      }
    }
  }
}`

func TestProjetarFiltraGrupoInativoEModulosInativos(t *testing.T) {
	var doc UsuarioDoc
	if err := json.Unmarshal([]byte(sampleDoc), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	p := Projetar(doc)

	if !p.Encontrado || p.Login != "10596693664" || p.Nome != "Daniel Ferraz" {
		t.Fatalf("cabeçalho do perfil inesperado: %+v", p)
	}
	if p.Email != "daniel.ferraz@azapfy.com.br" {
		t.Fatalf("email (cod_relator do SAC) não projetado: %q", p.Email)
	}
	if len(p.Empresas) != 1 {
		t.Fatalf("esperava 1 empresa ativa (AZAPERS), veio %d: %+v", len(p.Empresas), p.Empresas)
	}
	emp := p.Empresas[0]
	if emp.GrupoEmpresa != "AZAPERS" {
		t.Fatalf("esperava AZAPERS (AZAPFY é inativo), veio %q", emp.GrupoEmpresa)
	}
	if emp.GrupoUser != "COLABORADOR" {
		t.Fatalf("grupo_user inesperado: %q", emp.GrupoUser)
	}
	if len(emp.Bases) != 1 || emp.Bases[0].Sigla != "MAT" {
		t.Fatalf("esperava base MATRIZ/MAT: %+v", emp.Bases)
	}
	mods := emp.Bases[0].ModulosAtivos
	for _, m := range []string{"pesquisa", "rastreamento", "ocorrencia"} {
		if !contains(mods, m) {
			t.Fatalf("módulo ativo %q faltando em %v", m, mods)
		}
	}
	for _, m := range []string{"romaneio_automatico", "torre_controle"} {
		if contains(mods, m) {
			t.Fatalf("módulo inativo %q vazou em %v", m, mods)
		}
	}
}

func TestProjetarDeterministica(t *testing.T) {
	var doc UsuarioDoc
	if err := json.Unmarshal([]byte(sampleDoc), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	b1, _ := json.Marshal(Projetar(doc))
	b2, _ := json.Marshal(Projetar(doc))
	if string(b1) != string(b2) {
		t.Fatalf("projeção não-determinística:\n%s\n%s", b1, b2)
	}
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
