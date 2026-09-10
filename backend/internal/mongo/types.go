// Package mongo acessa o banco da Azapfy para resolver o usuário pelo login e
// projeta o documento no perfil mínimo (a 1ª "tool" do gateway).
package mongo

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
	Login  string              `bson:"login" json:"login"`
	Nome   string              `bson:"nome" json:"nome"`
	Email  string              `bson:"email" json:"email"`
	Grupos map[string]GrupoDoc `bson:"grupos" json:"grupos"`
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
