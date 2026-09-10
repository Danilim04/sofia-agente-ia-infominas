package mongo

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

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
// encontra; (zero, false, nil) quando não há documento.
func (r *Repo) BuscarPorLogin(ctx context.Context, login string) (UsuarioDoc, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	var doc UsuarioDoc
	err := r.coll.FindOne(cctx, bson.M{"login": login},
		options.FindOne().SetProjection(projection)).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return UsuarioDoc{}, false, nil
	}
	if err != nil {
		return UsuarioDoc{}, false, err
	}
	return doc, true, nil
}

// Close encerra a conexão com o Mongo.
func (r *Repo) Close(ctx context.Context) error { return r.client.Disconnect(ctx) }
