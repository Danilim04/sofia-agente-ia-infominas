"""Fonte de documentação local (docs/*.md) atrás da interface do sync.

A fonte da API/portal da Infominas entra em E3.2 (pendência P6).
"""

from __future__ import annotations

import pytest

from src.rag.chunking import chunkear_documento
from src.rag.fonte import DocumentoFonte, ErroDeFonte, FonteDocsLocais

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
