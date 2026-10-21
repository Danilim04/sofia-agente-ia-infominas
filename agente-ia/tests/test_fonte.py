"""Fontes de documentação atrás da interface do sync.

- `FonteDocsLocais`: `agente-ia/docs/base/*.md` (placeholder até a P6).
- `FonteAPIInfominas`: stub configurável da API/portal da Infominas (P6).
"""

from __future__ import annotations

import httpx
import pytest

from src.rag.chunking import chunkear_documento
from src.rag.fonte import (
    DIR_DOCS_BASE,
    DocumentoFonte,
    ErroDeFonte,
    FonteAPIInfominas,
    FonteDocsLocais,
    separar_front_matter,
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
    assert meta["source"] == "Cadastro de veículos"  # rótulo citável
    assert "Documentos obrigatórios" in meta["secao"]  # caminho de seção
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
# Base local placeholder (agente-ia/docs/base)
# ---------------------------------------------------------------------------


def test_fonte_local_padrao_le_os_tres_docs_placeholder():
    fonte = FonteDocsLocais()  # default = agente-ia/docs/base
    itens = fonte.listar()
    assert len(itens) == 3
    assert sorted(i.id for i in itens) == sorted(p.name for p in DIR_DOCS_BASE.glob("*.md"))
    for item in itens:
        raw = (DIR_DOCS_BASE / item.id).read_text(encoding="utf-8")
        meta, _ = separar_front_matter(raw)
        assert meta.get("placeholder") == "true", f"{item.id} sem placeholder: true"
        doc = fonte.obter(item.id)
        assert doc.titulo == meta["titulo"]  # rótulo de citação vem do front-matter
        assert not doc.conteudo.startswith("---")  # front-matter não vai para o chunk
        assert "placeholder" in doc.conteudo.lower()  # aviso visível no corpo


def test_docs_placeholder_nao_afirmam_preco_ou_prazo():
    for md in DIR_DOCS_BASE.glob("*.md"):
        texto = md.read_text(encoding="utf-8").lower()
        assert "r$" not in texto, md.name
        assert "garantimos" not in texto and "em até" not in texto, md.name


def test_docs_placeholder_geram_chunks_citaveis():
    fonte = FonteDocsLocais()
    for item in fonte.listar():
        chunks = chunkear_documento(fonte.obter(item.id))
        assert chunks
        assert all(c.metadata["source"] == item.titulo for c in chunks)
        assert all("placeholder: true" not in c.page_content for c in chunks)


def test_separar_front_matter_sem_front_matter_devolve_texto_intacto():
    assert separar_front_matter("# Título\n\ncorpo") == ({}, "# Título\n\ncorpo")
    assert separar_front_matter("---\nsem fechamento\n# x") == ({}, "---\nsem fechamento\n# x")


# ---------------------------------------------------------------------------
# FonteAPIInfominas (stub — TODO(P6))
# ---------------------------------------------------------------------------


def _sem_config_api(monkeypatch, tmp_path):
    for var in ("DOCS_API_BASE_URL", "DOCS_API_KEY", "DOCS_API_TIMEOUT"):
        monkeypatch.delenv(var, raising=False)
    monkeypatch.chdir(tmp_path)  # sem .env por perto


def test_api_infominas_sem_config_falha_com_mensagem_clara(monkeypatch, tmp_path):
    _sem_config_api(monkeypatch, tmp_path)
    with pytest.raises(ErroDeFonte, match=r"DOCS_API_\* não configurado"):
        FonteAPIInfominas()


def test_api_infominas_le_config_do_ambiente(monkeypatch, tmp_path):
    _sem_config_api(monkeypatch, tmp_path)
    monkeypatch.setenv("DOCS_API_BASE_URL", "https://docs.exemplo.test/v1/docs")
    monkeypatch.setenv("DOCS_API_KEY", "chave-de-teste")
    pedidos: list[httpx.Request] = []

    def handler(req: httpx.Request) -> httpx.Response:
        pedidos.append(req)
        return httpx.Response(200, json={"itens": []})

    fonte = FonteAPIInfominas(transport=httpx.MockTransport(handler))
    assert fonte.listar() == []
    assert str(pedidos[0].url).startswith("https://docs.exemplo.test/v1/docs")
    assert pedidos[0].headers["Authorization"] == "Bearer chave-de-teste"


def _api_falsa() -> httpx.MockTransport:
    itens = [
        {"id": "a", "titulo": "Doc A", "categoria": "Geral", "updated_at": "t1"},
        {"id": "b", "titulo": "Doc B", "categoria": "Geral", "updated_at": "t2", "deleted": True},
    ]

    def handler(req: httpx.Request) -> httpx.Response:
        path = req.url.path
        if path == "/docs":
            if req.url.params.get("cursor") == "p2":
                return httpx.Response(200, json={"itens": itens[1:]})
            return httpx.Response(200, json={"itens": itens[:1], "next_cursor": "p2"})
        if path == "/docs/a":
            corpo = {"id": "a", "titulo": "Doc A", "categoria": "Geral", "conteudo": "# A\n\ntexto"}
            return httpx.Response(200, json=corpo)
        return httpx.Response(404, json={})

    return httpx.MockTransport(handler)


def test_api_infominas_lista_paginado_e_obtem_documento():
    fonte = FonteAPIInfominas("https://api.exemplo.test/docs/", transport=_api_falsa())
    itens = fonte.listar()
    assert [(i.id, i.deleted) for i in itens] == [("a", False), ("b", True)]
    doc = fonte.obter("a")
    assert doc == DocumentoFonte(id="a", titulo="Doc A", categoria="Geral", conteudo="# A\n\ntexto")


@pytest.mark.parametrize("status", [401, 404, 500])
def test_api_infominas_status_de_erro_aborta_com_erro_de_fonte(status):
    transport = httpx.MockTransport(lambda req: httpx.Response(status, json={}))
    fonte = FonteAPIInfominas("https://api.exemplo.test/docs", transport=transport)
    with pytest.raises(ErroDeFonte, match=str(status)):
        fonte.listar()
    with pytest.raises(ErroDeFonte):
        fonte.obter("x")


def test_api_infominas_item_sem_campo_obrigatorio_e_fora_do_contrato():
    transport = httpx.MockTransport(
        lambda req: httpx.Response(200, json={"itens": [{"id": "a", "titulo": "A"}]})
    )
    fonte = FonteAPIInfominas("https://api.exemplo.test/docs", transport=transport)
    with pytest.raises(ErroDeFonte, match="categoria"):
        fonte.listar()


def test_api_infominas_falha_de_rede_vira_erro_de_fonte():
    def handler(req: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("sem rede (simulado)")

    fonte = FonteAPIInfominas(
        "https://api.exemplo.test/docs", transport=httpx.MockTransport(handler)
    )
    with pytest.raises(ErroDeFonte, match="falha de rede"):
        fonte.listar()
