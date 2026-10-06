// Package mongo acessa o banco da Azapfy para resolver o usuário pelo login e
// projeta o documento no perfil mínimo (a 1ª "tool" do gateway).
package mongo

import (
	"encoding/json"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
)

// Perfil é o perfil mínimo resolvido — mesmo formato do Contrato A
// (`identidade`): só empresas/bases com acesso, módulos ativos e o tipo de
// permissão (`grupo_user`). Tudo o mais do documento é descartado.
type Perfil struct {
	Encontrado bool      `json:"encontrado"`
	Login      string    `json:"login,omitempty"`
	Nome       string    `json:"nome,omitempty"`
	Email      string    `json:"email,omitempty"` // = cod_relator no SAC (abertura/listagem de chamados)
	Empresas   []Empresa `json:"empresas,omitempty"`
}

// Empresa é um grupo (grupo_empresa) com acesso ativo.
type Empresa struct {
	GrupoEmpresa string `json:"grupo_empresa"`
	GrupoUser    string `json:"grupo_user"`
	Area         string `json:"area"`
	Bases        []Base `json:"bases"`
}

// Base é uma base do grupo com os módulos ativos do usuário.
type Base struct {
	Nome          string   `json:"nome"`
	Sigla         string   `json:"sigla"`
	ModulosAtivos []string `json:"modulos_ativos"`
}

// TemEmpresaAtiva informa se há ao menos um grupo com acesso (após a projeção).
func (p Perfil) TemEmpresaAtiva() bool { return len(p.Empresas) > 0 }

// --- Documento bruto do Mongo (subconjunto que nos interessa) ---
// As tags bson são usadas pelo driver; as tags json permitem parsear o doc de
// exemplo nos testes sem subir um Mongo.

// UsuarioDoc é o documento do usuário no Mongo (campos mínimos).
type UsuarioDoc struct {
	Login  string    `bson:"login" json:"login"`
	Nome   string    `bson:"nome" json:"nome"`
	Email  string    `bson:"email" json:"email"`
	Grupos GruposDoc `bson:"grupos" json:"grupos"`
}

// GruposDoc é o campo `grupos` do documento: um MAPA nome→grupo na forma
// canônica, mas há cadastros em que vem como LISTA de grupos (conversa #32) —
// e um tipo inesperado quebrava a decodificação inteira do documento. Aceita
// as duas formas (BSON e JSON); na lista, o nome do grupo vem de
// `grupo_empresa`/`grupo`/`nome`/`empresa` (o primeiro preenchido) ou, sem
// nenhum, de `grupo_N` pela posição.
type GruposDoc map[string]GrupoDoc

// grupoListado é um elemento de `grupos` quando o campo é uma lista.
type grupoListado struct {
	GrupoDoc     `bson:",inline"`
	GrupoEmpresa string `bson:"grupo_empresa" json:"grupo_empresa"`
	Grupo        string `bson:"grupo" json:"grupo"`
	Nome         string `bson:"nome" json:"nome"`
	Empresa      string `bson:"empresa" json:"empresa"`
}

func (g grupoListado) chave(i int) string {
	for _, c := range []string{g.GrupoEmpresa, g.Grupo, g.Nome, g.Empresa} {
		if c != "" {
			return c
		}
	}
	return fmt.Sprintf("grupo_%d", i)
}

func gruposDeLista(lista []grupoListado) GruposDoc {
	out := make(GruposDoc, len(lista))
	for i, g := range lista {
		out[g.chave(i)] = g.GrupoDoc
	}
	return out
}

// UnmarshalBSONValue aceita `grupos` como documento (mapa) ou array (lista);
// null/ausente vira mapa vazio.
func (g *GruposDoc) UnmarshalBSONValue(t bsontype.Type, data []byte) error {
	raw := bson.RawValue{Type: t, Value: data}
	switch t {
	case bsontype.Null, bsontype.Undefined:
		*g = GruposDoc{}
		return nil
	case bsontype.Array:
		var lista []grupoListado
		if err := raw.Unmarshal(&lista); err != nil {
			return fmt.Errorf("grupos (lista): %w", err)
		}
		*g = gruposDeLista(lista)
		return nil
	case bsontype.EmbeddedDocument:
		var mapa map[string]GrupoDoc
		if err := raw.Unmarshal(&mapa); err != nil {
			return fmt.Errorf("grupos (mapa): %w", err)
		}
		*g = GruposDoc(mapa)
		return nil
	default:
		return fmt.Errorf("grupos: tipo BSON inesperado %s", t)
	}
}

// UnmarshalJSON é o espelho do BSON para os fixtures/testes em JSON.
func (g *GruposDoc) UnmarshalJSON(b []byte) error {
	trimmed := bytesTrim(b)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*g = GruposDoc{}
		return nil
	}
	if trimmed[0] == '[' {
		var lista []grupoListado
		if err := json.Unmarshal(b, &lista); err != nil {
			return fmt.Errorf("grupos (lista): %w", err)
		}
		*g = gruposDeLista(lista)
		return nil
	}
	var mapa map[string]GrupoDoc
	if err := json.Unmarshal(b, &mapa); err != nil {
		return fmt.Errorf("grupos (mapa): %w", err)
	}
	*g = GruposDoc(mapa)
	return nil
}

func bytesTrim(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\t' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}

// GrupoDoc é uma entrada de `grupos` (chave = nome do grupo_empresa).
type GrupoDoc struct {
	Ativo     bool               `bson:"ativo" json:"ativo"`
	GrupoUser string             `bson:"grupo_user" json:"grupo_user"`
	Area      string             `bson:"area" json:"area"`
	Bases     map[string]BaseDoc `bson:"bases" json:"bases"`
}

// BaseDoc é uma entrada de `bases` (chave = nome da base).
type BaseDoc struct {
	Nome    string               `bson:"nome" json:"nome"`
	Sigla   string               `bson:"sigla" json:"sigla"`
	Modulos map[string]ModuloDoc `bson:"modulos" json:"modulos"`
}

// ModuloDoc é uma entrada de `modulos` (chave = nome do módulo).
type ModuloDoc struct {
	Ativo bool `bson:"ativo" json:"ativo"`
	Web   bool `bson:"web" json:"web"`
}
