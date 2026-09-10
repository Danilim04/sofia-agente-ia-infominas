"""Fontes de documentação (Contrato B): local (docs/*.md) e API do cliente.

A fonte API usa `httpx.MockTransport` — nenhum teste faz rede.
"""

from __future__ import annotations

import httpx
import pytest

from src.rag.chunking import chunkear_documento
from src.rag.fonte import (
    DocumentoFonte,
    DocumentoRemovido,
    ErroDeFonte,
    FonteAPIDocumentacao,
    FonteDocsLocais,
)


# ---------------------------------------------------------------------------
# Chunking — contrato de metadados das citações
# ---------------------------------------------------------------------------

def test_chunkear_documento_gera_metadados_citaveis():
    doc = DocumentoFonte(
        id="documentacao/frotas/cadastro-veiculos.md",
        titulo="Cadastro de veículos",
        categoria="Frotas",
        conteudo="# Cadastro\n\n## Documentos obrigatórios\n\nCRLV e apólice.",
    )
    chunks = chunkear_documento(doc)
    assert chunks
    meta = chunks[-1].metadata
    assert meta["source"] == "Cadastro de veículos"      # rótulo citável
    assert "Documentos obrigatórios" in meta["secao"]     # caminho de seção
    assert meta["categoria"] == "Frotas"
    assert meta["doc_id"] == "documentacao/frotas/cadastro-veiculos.md"


def test_chunkear_documento_vazio_retorna_lista_vazia():
    doc = DocumentoFonte(id="x", titulo="X", categoria="C", conteudo="   \n ")
    assert chunkear_documento(doc) == []


# ---------------------------------------------------------------------------
# Fonte local (docs/*.md)
# ---------------------------------------------------------------------------

def test_fonte_local_lista_e_obtem(tmp_path):
    (tmp_path / "a.md").write_text("# Doc A\n\nConteúdo A.", encoding="utf-8")
    (tmp_path / "b.md").write_text("# Doc B\n\nConteúdo B.", encoding="utf-8")
    (tmp_path / "vazio.md").write_text("", encoding="utf-8")

    fonte = FonteDocsLocais(tmp_path)
    itens = fonte.listar()
    assert [i.id for i in itens] == ["a.md", "b.md"]  # vazio é pulado
    assert all(not i.deleted for i in itens)

    doc = fonte.obter("a.md")
    assert doc.titulo == "a.md" and "Conteúdo A" in doc.conteudo


def test_fonte_local_fingerprint_muda_so_com_conteudo(tmp_path):
    arq = tmp_path / "a.md"
    arq.write_text("# Doc\n\nv1", encoding="utf-8")
    fonte = FonteDocsLocais(tmp_path)

    fp1 = fonte.listar()[0].updated_at
    assert fonte.listar()[0].updated_at == fp1  # determinístico (imune a mtime)

    arq.write_text("# Doc\n\nv2", encoding="utf-8")
    assert fonte.listar()[0].updated_at != fp1


def test_fonte_local_nao_sai_do_diretorio(tmp_path):
    (tmp_path / "a.md").write_text("# A", encoding="utf-8")
    fonte = FonteDocsLocais(tmp_path)
    with pytest.raises(ErroDeFonte):
        fonte.obter("../fora.md")


# ---------------------------------------------------------------------------
# Fonte API (Contrato B — implementação AzapDocs)
# ---------------------------------------------------------------------------

BASE = "http://fonte.teste/api/v1/integrations/docs"
COLECAO = "/api/v1/integrations/docs"

def _item(i, **extra):
    return {
        "id": f"documentacao/frotas/doc-{i}.md",
        "titulo": f"Doc {i}",
        "categoria": "Frotas",
        "updated_at": "2026-08-30T14:22:00Z",
        "deleted": False,
        **extra,
    }


def _fonte_com(handler) -> FonteAPIDocumentacao:
    return FonteAPIDocumentacao(
        base_url=BASE + "/",  # barra final tolerada
        api_key="azk_teste",
        transport=httpx.MockTransport(handler),
    )


def _me(**sobrescreve):
    espaco = {
        "id": "5f2", "nome": "Zapin - Base de Conhecimento",
        "escopos": ["DOCS_LIST", "DOCS_READ"], "ativo": True,
        "apiHabilitada": True, "espacoArquivado": False, "donoTemAcesso": True,
    }
    espaco.update(sobrescreve.pop("espaco", {}))
    base = {
        "user": {"id": "u1", "name": "Gustavo Penido", "role": "HEAD_DEV"},
        "apiKey": {"id": "k1", "scopes": ["DOCS_LIST", "DOCS_READ"]},
        "espacos": [espaco],
        "contrato": {"versao": "1.0", "retencaoTombstoneDias": 60},
    }
    base.update(sobrescreve)
    return base


def test_fonte_api_listagem_paginada_e_tombstone():
    def handler(request: httpx.Request) -> httpx.Response:
        # Chave azk_ no header X-API-Key; inventário é GET na COLEÇÃO (sem /docs extra).
        assert request.headers["X-API-Key"] == "azk_teste"
        assert "Authorization" not in request.headers
        assert request.url.path == COLECAO
        assert request.url.params.get("limit") == "500"
        cursor = request.url.params.get("cursor")
        if cursor is None:
            return httpx.Response(200, json={
                "itens": [_item(1), _item(2, deleted=True, deleted_at="2026-08-28T09:10:00Z")],
                "next_cursor": "p2",
            })
        assert cursor == "p2"
        return httpx.Response(200, json={"itens": [_item(3)], "next_cursor": None})

    itens = _fonte_com(handler).listar()
    assert len(itens) == 3
    assert itens[1].deleted is True
    assert itens[2].id == "documentacao/frotas/doc-3.md"


def test_fonte_api_5xx_vira_erro_de_fonte():
    fonte = _fonte_com(lambda req: httpx.Response(503, text="indisponível"))
    with pytest.raises(ErroDeFonte):
        fonte.listar()


def test_fonte_api_item_sem_campo_obrigatorio_aborta():
    """§4.2 do contrato: resposta fora do contrato aborta — nunca 'aproveitar'."""
    def handler(request):
        quebrado = {k: v for k, v in _item(1).items() if k != "updated_at"}
        return httpx.Response(200, json={"itens": [quebrado], "next_cursor": None})

    with pytest.raises(ErroDeFonte):
        _fonte_com(handler).listar()


def test_fonte_api_401_aborta_com_status_legivel():
    fonte = _fonte_com(lambda req: httpx.Response(401, json={
        "message": 'Envie a chave em "X-API-Key: azk_..."', "statusCode": 401}))
    with pytest.raises(ErroDeFonte, match="401"):
        fonte.listar()


def test_fonte_api_obter_bate_no_id_sob_a_colecao():
    visto = {}

    def handler(request: httpx.Request) -> httpx.Response:
        visto["path"] = request.url.path
        return httpx.Response(200, json={
            "id": "8c1e", "titulo": "Cadastro de veículos", "categoria": "Frotas",
            "conteudo": "# Cadastro de veículos\n\n## Documentos obrigatórios\n…",
            "updated_at": "2026-08-30T14:22:00.000Z", "deleted": False,
        })

    doc = _fonte_com(handler).obter("8c1e")
    assert visto["path"] == COLECAO + "/8c1e"
    assert doc.titulo == "Cadastro de veículos" and doc.categoria == "Frotas"


def test_fonte_api_410_vira_documento_removido():
    """Doc virou tombstone entre a listagem e a leitura: erro específico (o
    sync conta como falha do doc; a remoção vem pela listagem seguinte)."""
    fonte = _fonte_com(lambda req: httpx.Response(410, json={"statusCode": 410}))
    with pytest.raises(DocumentoRemovido):
        fonte.obter("1a77")


def test_fonte_api_404_fora_da_chave_e_erro_generico():
    fonte = _fonte_com(lambda req: httpx.Response(404, json={"statusCode": 404}))
    with pytest.raises(ErroDeFonte) as exc:
        fonte.obter("zzz")
    assert not isinstance(exc.value, DocumentoRemovido)


def test_fonte_api_diagnostico_me_pronto():
    visto = {}

    def handler(request: httpx.Request) -> httpx.Response:
        visto["path"] = request.url.path
        return httpx.Response(200, json=_me())

    diag = _fonte_com(handler).diagnosticar()
    assert visto["path"] == COLECAO + "/me"
    assert diag.pronto and diag.problemas() == []
    assert diag.usuario == "Gustavo Penido"
    assert diag.contrato_versao == "1.0" and diag.retencao_tombstone_dias == 60
    assert diag.espacos[0].nome == "Zapin - Base de Conhecimento"


@pytest.mark.parametrize("sobrescreve, trecho", [
    ({"espaco": {"apiHabilitada": False}}, "API desabilitada"),
    ({"espaco": {"donoTemAcesso": False}}, "sem acesso"),
    ({"espaco": {"ativo": False}}, "inativo"),
    ({"espaco": {"espacoArquivado": True}}, "arquivado"),
    ({"espaco": {"escopos": ["DOCS_LIST"]}}, "DOCS_READ"),
    ({"apiKey": {"id": "k1", "scopes": ["DOCS_LIST"]}}, "chave sem os escopos"),
    ({"espacos": []}, "nenhum espaço"),
])
def test_fonte_api_diagnostico_aponta_a_condicao_que_caiu(sobrescreve, trecho):
    diag = _fonte_com(lambda req: httpx.Response(200, json=_me(**sobrescreve))).diagnosticar()
    assert not diag.pronto
    assert any(trecho in p for p in diag.problemas()), diag.problemas()


def test_fonte_api_obter_codifica_id_com_barras():
    visto = {}

    def handler(request: httpx.Request) -> httpx.Response:
        visto["path"] = request.url.raw_path.decode()
        return httpx.Response(200, json={
            "id": "documentacao/frotas/doc-1.md",
            "titulo": "Doc 1",
            "categoria": "Frotas",
            "conteudo": "# Doc 1\n\nTexto.",
            "updated_at": "2026-08-30T14:22:00Z",
            "deleted": False,
        })

    doc = _fonte_com(handler).obter("documentacao/frotas/doc-1.md")
    assert doc.conteudo.startswith("# Doc 1")
    assert "%2F" in visto["path"]  # id com barras vai URL-encodado no path


def test_fonte_api_resposta_nao_json_aborta():
    fonte = _fonte_com(lambda req: httpx.Response(200, text="<html>proxy</html>"))
    with pytest.raises(ErroDeFonte):
        fonte.listar()
