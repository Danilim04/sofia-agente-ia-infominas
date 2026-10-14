package mongo

import (
	"encoding/json"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
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

// docGruposLista reproduz o cadastro da conversa #32: `grupos` como LISTA.
const docGruposLista = `{
  "login": "32",
  "nome": "Cliente Trinta e Dois",
  "email": "c32@x.com",
  "grupos": [
    {"grupo_empresa": "MEARIM", "ativo": true, "grupo_user": "CLIENTE", "area": "SAC",
     "bases": {"MATRIZ": {"nome": "MATRIZ", "sigla": "MAT", "modulos": {"pesquisa": {"ativo": true}}}}},
    {"nome": "INATIVO", "ativo": false},
    {"ativo": true}
  ]
}`

// TestGruposEmListaLidoSemErro (ZC-4): a lista é aceita e projetada como o
// mapa — grupo ativo entra, inativo fica fora, sem nome ganha chave posicional.
func TestGruposEmListaLidoSemErro(t *testing.T) {
	var doc UsuarioDoc
	if err := json.Unmarshal([]byte(docGruposLista), &doc); err != nil {
		t.Fatalf("unmarshal JSON com grupos em lista: %v", err)
	}
	if len(doc.Grupos) != 3 {
		t.Fatalf("esperava 3 grupos, veio %d: %+v", len(doc.Grupos), doc.Grupos)
	}
	if _, ok := doc.Grupos["grupo_2"]; !ok {
		t.Fatalf("grupo sem nome devia ganhar chave posicional: %+v", doc.Grupos)
	}
	p := Projetar(doc)
	if len(p.Empresas) != 2 || p.Empresas[0].GrupoEmpresa != "MEARIM" || p.Empresas[0].GrupoUser != "CLIENTE" {
		t.Fatalf("projeção da lista inesperada: %+v", p.Empresas)
	}
	if len(p.Empresas[0].Bases) != 1 || p.Empresas[0].Bases[0].Sigla != "MAT" {
		t.Fatalf("bases do grupo listado não projetadas: %+v", p.Empresas[0].Bases)
	}
}

// TestGruposBSONAceitaListaEMapa: o caminho REAL é o driver BSON — o mesmo
// documento serializado com `grupos` em array e em documento decodifica.
func TestGruposBSONAceitaListaEMapa(t *testing.T) {
	lista := bson.D{
		{Key: "login", Value: "32"}, {Key: "nome", Value: "C"}, {Key: "email", Value: "c@x.com"},
		{Key: "grupos", Value: bson.A{
			bson.D{{Key: "grupo_empresa", Value: "MEARIM"}, {Key: "ativo", Value: true}, {Key: "grupo_user", Value: "CLIENTE"}},
			bson.D{{Key: "grupo", Value: "OUTRO"}, {Key: "ativo", Value: false}},
		}},
	}
	mapa := bson.D{
		{Key: "login", Value: "33"}, {Key: "nome", Value: "D"}, {Key: "email", Value: "d@x.com"},
		{Key: "grupos", Value: bson.D{
			{Key: "AZAPERS", Value: bson.D{{Key: "ativo", Value: true}, {Key: "grupo_user", Value: "COLABORADOR"}}},
		}},
	}
	nulo := bson.D{{Key: "login", Value: "34"}, {Key: "grupos", Value: nil}}

	for nome, d := range map[string]bson.D{"lista": lista, "mapa": mapa, "nulo": nulo} {
		raw, err := bson.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var doc UsuarioDoc
		if err := bson.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: unmarshal BSON: %v", nome, err)
		}
		switch nome {
		case "lista":
			if g, ok := doc.Grupos["MEARIM"]; !ok || !g.Ativo || g.GrupoUser != "CLIENTE" || len(doc.Grupos) != 2 {
				t.Fatalf("lista BSON mal lida: %+v", doc.Grupos)
			}
		case "mapa":
			if g, ok := doc.Grupos["AZAPERS"]; !ok || !g.Ativo || g.GrupoUser != "COLABORADOR" {
				t.Fatalf("mapa BSON mal lido: %+v", doc.Grupos)
			}
		case "nulo":
			if doc.Grupos == nil || len(doc.Grupos) != 0 {
				t.Fatalf("grupos nulo devia virar mapa vazio: %+v", doc.Grupos)
			}
		}
	}
}
