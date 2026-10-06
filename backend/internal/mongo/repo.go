package mongo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ErrDocumentoInvalido marca um documento que EXISTE mas não pôde ser lido no
// formato esperado (ex.: campo com tipo inesperado). Diferente de "Mongo fora":
// tentar de novo não resolve — o gate transfere ao atendente na hora.
var ErrDocumentoInvalido = errors.New("documento do usuário em formato inesperado")

// Repo acessa a coleção de usuários do Mongo da Azapfy.
type Repo struct {
	client  *mongo.Client
	coll    *mongo.Collection
	timeout time.Duration
}

// NewRepo conecta ao Mongo e devolve o repositório.
func NewRepo(ctx context.Context, uri, db, coll string, timeout time.Duration) (*Repo, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := mongo.Connect(cctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	return &Repo{
		client:  client,
		coll:    client.Database(db).Collection(coll),
		timeout: timeout,
	}, nil
}

// projection limita os campos lidos ao mínimo necessário (data minimization):
// nunca trazemos cpf/senha/app/etc. para o processo.
var projection = bson.M{"login": 1, "nome": 1, "email": 1, "grupos": 1}

// BuscarPorLogin busca o usuário pelo login. Devolve (doc, true, nil) quando
// encontra; (zero, false, nil) quando não há documento; erro de decodificação
// sai embrulhado em ErrDocumentoInvalido (conversa #32: `grupos` em lista).
func (r *Repo) BuscarPorLogin(ctx context.Context, login string) (UsuarioDoc, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	res := r.coll.FindOne(cctx, bson.M{"login": login}, options.FindOne().SetProjection(projection))
	if err := res.Err(); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return UsuarioDoc{}, false, nil
		}
		return UsuarioDoc{}, false, err
	}
	var doc UsuarioDoc
	if err := res.Decode(&doc); err != nil {
		return UsuarioDoc{}, false, fmt.Errorf("%w: %v", ErrDocumentoInvalido, err)
	}
	return doc, true, nil
}

// BuscarPorLogins busca de uma vez todos os candidatos de login (`$in`) — o
// gate deriva vários candidatos de uma frase e, em vez de N idas ao Mongo,
// faz uma só. O prazo vem do ctx do chamador (o gate usa 3 s), limitado pelo
// timeout do repo. Documento ilegível vira ErrDocumentoInvalido.
func (r *Repo) BuscarPorLogins(ctx context.Context, logins []string) ([]UsuarioDoc, error) {
	if len(logins) == 0 {
		return nil, nil
	}
	cctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cur, err := r.coll.Find(cctx, bson.M{"login": bson.M{"$in": logins}},
		options.Find().SetProjection(projection).SetLimit(int64(len(logins))))
	if err != nil {
		return nil, err
	}
	defer cur.Close(cctx)
	var docs []UsuarioDoc
	for cur.Next(cctx) {
		var doc UsuarioDoc
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDocumentoInvalido, err)
		}
		docs = append(docs, doc)
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}

// Close encerra a conexão com o Mongo.
func (r *Repo) Close(ctx context.Context) error { return r.client.Disconnect(ctx) }
