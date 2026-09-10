"""Integridade da sincronização incremental (Contrato B, seção 5).

Cada cenário da tabela do contrato vira um teste — fonte, índice e embeddings
são fakes (sem rede, sem Postgres). O que estes testes garantem:

- o sinal de mudança é o hash dos METADADOS da listagem (updated_at DENTRO,
  por decisão de projeto — cenário 5 reprocessa "à toa" de propósito);
- deleção só por tombstone; ausência NUNCA deleta;
- travas anti-catástrofe rodam ANTES de qualquer mutação;
- falha em um documento não derruba o ciclo e a reexecução converge.
"""

from __future__ import annotations

import hashlib

import pytest

from src.rag.fonte import DocumentoFonte, ErroDeFonte, ItemListagem, RegistroDoc
from src.rag.sync import (
    ErroModeloIncompativel,
    TravaRemocaoMassa,
    executar_sync,
    hash_listagem,
    planejar,
)


# ---------------------------------------------------------------------------
# Fakes (mesma interface dos reais; ver protocolos em src/rag/sync.py)
# ---------------------------------------------------------------------------

MODELO = "fake-model"


class FakeEmbeddings:
    """Vetores determinísticos por texto; conta chamadas p/ asserção de custo."""

    def __init__(self):
        self.textos_embedados = 0

    def _vec(self, texto: str) -> list[float]:
        digest = hashlib.md5(texto.encode("utf-8")).digest()
        return [b / 255.0 for b in digest[:8]]

    def embed_documents(self, textos):
        self.textos_embedados += len(textos)
        return [self._vec(t) for t in textos]

    def embed_query(self, texto):
        return self._vec(texto)


class FakeFonte:
    """Fonte em memória; `docs` é mutável entre execuções do sync.

    docs: dict[id, dict(titulo, categoria, updated_at, conteudo, deleted)]
    """

    def __init__(self, docs: dict):
        self.docs = docs
        self.obter_chamados: list[str] = []
        self.falhar_obter: set[str] = set()
        self.falhar_listar = False

    def listar(self):
        if self.falhar_listar:
            raise ErroDeFonte("fonte indisponível (simulado)")
        return [
            ItemListagem(
                id=doc_id,
                titulo=d["titulo"],
                categoria=d["categoria"],
                updated_at=d["updated_at"],
                deleted=d.get("deleted", False),
            )
            for doc_id, d in self.docs.items()
        ]

    def obter(self, doc_id: str):
        self.obter_chamados.append(doc_id)
        if doc_id in self.falhar_obter:
            raise ErroDeFonte(f"falha simulada ao obter {doc_id}")
        d = self.docs[doc_id]
        return DocumentoFonte(
            id=doc_id,
            titulo=d["titulo"],
            categoria=d["categoria"],
            conteudo=d["conteudo"],
        )


class FakeIndice:
    def __init__(self):
        self.chunks: dict[str, list] = {}
        self.controle: dict[str, RegistroDoc] = {}
        self.modelo: str | None = None
        self.limpezas = 0

    def preparar_esquema(self, embedding_model, dim):
        if self.modelo is None:
            self.modelo = embedding_model

    def modelo_registrado(self):
        return self.modelo

    def limpar_tudo(self, embedding_model, dim):
        self.limpezas += 1
        self.chunks.clear()
        self.controle.clear()
        self.modelo = embedding_model

    def estado(self):
        return dict(self.controle)

    def substituir_documento(self, item, hash_l, chunks, vetores):
        assert len(chunks) == len(vetores), "chunk sem vetor (integridade)"
        self.chunks[item.id] = chunks
        self.controle[item.id] = RegistroDoc(hash_listagem=hash_l, deleted=False)

    def remover_documento(self, doc_id, hash_l):
        self.chunks.pop(doc_id, None)
        self.controle[doc_id] = RegistroDoc(hash_listagem=hash_l, deleted=True)


def _doc(titulo="Cadastro de veículos", categoria="Frotas", updated_at="v1",
         conteudo="# Cadastro de veículos\n\nPasso a passo do cadastro.",
         deleted=False):
    return {
        "titulo": titulo,
        "categoria": categoria,
        "updated_at": updated_at,
        "conteudo": conteudo,
        "deleted": deleted,
    }


def _sync(fonte, indice, embeddings=None, **kwargs):
    kwargs.setdefault("embedding_model", MODELO)
    return executar_sync(fonte, indice, embeddings or FakeEmbeddings(), **kwargs)


# ---------------------------------------------------------------------------
# Hash de listagem — o sinal de mudança
# ---------------------------------------------------------------------------

def test_hash_listagem_inclui_updated_at_titulo_categoria_e_deleted():
    base = ItemListagem(id="a", titulo="T", categoria="C", updated_at="v1")
    assert hash_listagem(base) == hash_listagem(base)
    variantes = [
        ItemListagem(id="a", titulo="T2", categoria="C", updated_at="v1"),
        ItemListagem(id="a", titulo="T", categoria="C2", updated_at="v1"),
        ItemListagem(id="a", titulo="T", categoria="C", updated_at="v2"),
        ItemListagem(id="a", titulo="T", categoria="C", updated_at="v1", deleted=True),
    ]
    for v in variantes:
        assert hash_listagem(v) != hash_listagem(base)


# ---------------------------------------------------------------------------
# Cenários 1–5: novo, alterado, metadado, sem mudança, save sem mudança real
# ---------------------------------------------------------------------------

def test_cenario1_documento_novo_e_indexado():
    fonte = FakeFonte({"a.md": _doc()})
    indice = FakeIndice()
    r = _sync(fonte, indice)
    assert r.novos == 1 and r.ok
    assert "a.md" in indice.chunks and len(indice.chunks["a.md"]) >= 1
    assert not indice.controle["a.md"].deleted
    # Metadados citáveis (contrato do output_guardrail): source = titulo.
    assert indice.chunks["a.md"][0].metadata["source"] == "Cadastro de veículos"


def test_cenario4_sem_mudanca_nem_busca_conteudo():
    fonte = FakeFonte({"a.md": _doc()})
    indice = FakeIndice()
    emb = FakeEmbeddings()
    _sync(fonte, indice, emb)
    busca_apos_primeiro = len(fonte.obter_chamados)
    embeds_apos_primeiro = emb.textos_embedados

    r2 = _sync(fonte, indice, emb)
    assert r2.inalterados == 1 and r2.novos == 0 and r2.alterados == 0
    assert len(fonte.obter_chamados) == busca_apos_primeiro  # custo ≈ zero
    assert emb.textos_embedados == embeds_apos_primeiro


def test_cenario2_conteudo_alterado_substitui_chunks():
    fonte = FakeFonte({"a.md": _doc()})
    indice = FakeIndice()
    _sync(fonte, indice)

    fonte.docs["a.md"] = _doc(
        updated_at="v2", conteudo="# Cadastro de veículos\n\nFluxo NOVO do cadastro."
    )
    r = _sync(fonte, indice)
    assert r.alterados == 1
    assert "NOVO" in indice.chunks["a.md"][0].page_content
    assert all("Passo a passo" not in c.page_content for c in indice.chunks["a.md"])


def test_cenario3_so_metadado_alterado_reprocessa():
    fonte = FakeFonte({"a.md": _doc()})
    indice = FakeIndice()
    _sync(fonte, indice)

    fonte.docs["a.md"]["categoria"] = "Frotas › Veículos"
    fonte.docs["a.md"]["updated_at"] = "v2"
    r = _sync(fonte, indice)
    assert r.alterados == 1
    assert indice.chunks["a.md"][0].metadata["categoria"] == "Frotas › Veículos"


def test_cenario5_save_sem_mudanca_real_reprocessa_mesmo_assim():
    """Decisão de projeto: updated_at DENTRO do hash → rebuild à toa é aceito."""
    fonte = FakeFonte({"a.md": _doc()})
    indice = FakeIndice()
    _sync(fonte, indice)

    fonte.docs["a.md"]["updated_at"] = "v2"  # conteúdo idêntico
    r = _sync(fonte, indice)
    assert r.alterados == 1  # reprocessou, não pulou


# ---------------------------------------------------------------------------
# Cenários 6–8: deletado, restaurado, renomeado
# ---------------------------------------------------------------------------

def test_cenario6_tombstone_remove_documento():
    fonte = FakeFonte({"a.md": _doc(), "b.md": _doc(titulo="Avisos RNTRC")})
    indice = FakeIndice()
    _sync(fonte, indice)

    fonte.docs["a.md"]["deleted"] = True
    r = _sync(fonte, indice)
    assert r.removidos == 1
    assert "a.md" not in indice.chunks
    assert indice.controle["a.md"].deleted
    assert "b.md" in indice.chunks  # vizinho intacto


def test_cenario6_tombstone_repetido_e_ignorado():
    fonte = FakeFonte({"a.md": _doc(deleted=True)})
    indice = FakeIndice()
    r = _sync(fonte, indice)  # tombstone de doc nunca indexado
    assert r.removidos == 0 and r.ok


def test_cenario7_documento_restaurado_reindexado():
    fonte = FakeFonte({"a.md": _doc()})
    indice = FakeIndice()
    _sync(fonte, indice)
    fonte.docs["a.md"]["deleted"] = True
    _sync(fonte, indice)

    fonte.docs["a.md"]["deleted"] = False
    fonte.docs["a.md"]["updated_at"] = "v3"
    r = _sync(fonte, indice)
    assert r.alterados == 1
    assert "a.md" in indice.chunks
    assert not indice.controle["a.md"].deleted


def test_cenario8_renomeado_tombstone_antigo_mais_novo():
    fonte = FakeFonte({"antigo.md": _doc()})
    indice = FakeIndice()
    _sync(fonte, indice)

    fonte.docs["antigo.md"]["deleted"] = True
    fonte.docs["novo.md"] = _doc(updated_at="v9")
    r = _sync(fonte, indice)
    assert r.removidos == 1 and r.novos == 1
    assert "antigo.md" not in indice.chunks and "novo.md" in indice.chunks


# ---------------------------------------------------------------------------
# Cenários 9–11 + anomalias: falha da fonte, massa, listagem vazia, ausência
# ---------------------------------------------------------------------------

def test_cenario9_falha_da_fonte_aborta_com_indice_intacto():
    fonte = FakeFonte({"a.md": _doc()})
    indice = FakeIndice()
    _sync(fonte, indice)
    antes = dict(indice.controle)

    fonte.falhar_listar = True
    with pytest.raises(ErroDeFonte):
        _sync(fonte, indice)
    assert indice.controle == antes and "a.md" in indice.chunks


def test_cenario10_trava_remocao_em_massa():
    docs = {f"d{i}.md": _doc(titulo=f"Doc {i}") for i in range(10)}
    fonte = FakeFonte(docs)
    indice = FakeIndice()
    _sync(fonte, indice)

    for i in range(5):  # 50% da base vira tombstone (teto p/ 10 vivos = 2)
        fonte.docs[f"d{i}.md"]["deleted"] = True
    with pytest.raises(TravaRemocaoMassa):
        _sync(fonte, indice)
    assert len(indice.chunks) == 10  # nada foi removido

    r = _sync(fonte, indice, permitir_remocao_em_massa=True)
    assert r.removidos == 5 and len(indice.chunks) == 5


def test_cenario10_trava_avaliada_antes_de_qualquer_mutacao():
    docs = {f"d{i}.md": _doc(titulo=f"Doc {i}") for i in range(10)}
    fonte = FakeFonte(docs)
    indice = FakeIndice()
    _sync(fonte, indice)

    for i in range(5):
        fonte.docs[f"d{i}.md"]["deleted"] = True
    fonte.docs["novo.md"] = _doc()  # haveria trabalho novo no mesmo ciclo
    fonte.obter_chamados.clear()
    with pytest.raises(TravaRemocaoMassa):
        _sync(fonte, indice)
    assert fonte.obter_chamados == []  # nem o processamento começou


def test_cenario11_listagem_vazia_com_base_nao_vazia_aborta():
    fonte = FakeFonte({"a.md": _doc()})
    indice = FakeIndice()
    _sync(fonte, indice)

    fonte.docs.clear()
    with pytest.raises(TravaRemocaoMassa):
        _sync(fonte, indice)
    assert "a.md" in indice.chunks


def test_ausencia_da_listagem_nao_remove():
    """Contrato B: doc ausente SEM tombstone é anomalia, nunca deleção."""
    fonte = FakeFonte({"a.md": _doc(), "b.md": _doc(titulo="Avisos RNTRC")})
    indice = FakeIndice()
    _sync(fonte, indice)

    del fonte.docs["a.md"]  # sumiu sem tombstone (ex.: filtro bugado)
    r = _sync(fonte, indice)
    assert r.removidos == 0 and r.ausentes == 1
    assert "a.md" in indice.chunks  # conhecimento preservado


def test_remover_ausentes_explicito_para_fonte_local():
    fonte = FakeFonte({"a.md": _doc(), "b.md": _doc(titulo="Avisos RNTRC")})
    indice = FakeIndice()
    _sync(fonte, indice)

    del fonte.docs["a.md"]
    r = _sync(fonte, indice, remover_ausentes=True)
    assert r.removidos == 1 and "a.md" not in indice.chunks


# ---------------------------------------------------------------------------
# Robustez: falha parcial, doc vazio, modelo trocado, dry-run, primeira carga
# ---------------------------------------------------------------------------

def test_falha_em_um_doc_nao_derruba_ciclo_e_reexecucao_converge():
    fonte = FakeFonte({"a.md": _doc(), "b.md": _doc(titulo="Avisos RNTRC")})
    indice = FakeIndice()

    fonte.falhar_obter = {"b.md"}
    r1 = _sync(fonte, indice)
    assert r1.novos == 1 and r1.falhas == 1 and not r1.ok
    assert "a.md" in indice.chunks and "b.md" not in indice.chunks

    fonte.falhar_obter = set()
    r2 = _sync(fonte, indice)  # idempotência: só o pendente é reprocessado
    assert r2.novos == 1 and r2.inalterados == 1 and r2.ok
    assert "b.md" in indice.chunks


def test_documento_vazio_conta_falha_e_preserva_versao_indexada():
    fonte = FakeFonte({"a.md": _doc()})
    indice = FakeIndice()
    _sync(fonte, indice)

    fonte.docs["a.md"].update(conteudo="   ", updated_at="v2")
    r = _sync(fonte, indice)
    assert r.falhas == 1
    assert "a.md" in indice.chunks  # sumir com conhecimento exige tombstone


def test_modelo_de_embeddings_trocado_recusa_incremental():
    fonte = FakeFonte({"a.md": _doc()})
    indice = FakeIndice()
    _sync(fonte, indice)

    with pytest.raises(ErroModeloIncompativel):
        _sync(fonte, indice, embedding_model="outro-modelo")
    assert indice.modelo == MODELO  # nada mudou

    r = _sync(fonte, indice, embedding_model="outro-modelo", full=True)
    assert indice.limpezas == 1 and indice.modelo == "outro-modelo"
    assert r.novos == 1  # tudo re-embedado no espaço novo


def test_modelo_trocado_reconstroi_sozinho_quando_o_deploy_pede():
    """--reconstruir-se-modelo-mudou (usado pelo deploy): troca de
    EMBEDDINGS_MODEL refaz o índice inteiro em vez de abortar; com o mesmo
    modelo, a flag não muda nada (ciclo incremental normal)."""
    fonte = FakeFonte({"a.md": _doc(), "b.md": _doc()})
    indice = FakeIndice()
    _sync(fonte, indice)

    r = _sync(fonte, indice, embedding_model="openai/text-embedding-3-small",
              reconstruir_se_modelo_mudou=True)
    assert indice.limpezas == 1 and indice.modelo == "openai/text-embedding-3-small"
    assert r.novos == 2 and r.falhas == 0

    r = _sync(fonte, indice, embedding_model="openai/text-embedding-3-small",
              reconstruir_se_modelo_mudou=True)
    assert indice.limpezas == 1 and r.inalterados == 2  # sem troca: incremental

    # dry-run com troca: avisa o plano, mas não limpa nada
    indice2 = FakeIndice(); _sync(fonte, indice2)
    r = _sync(fonte, indice2, embedding_model="x", reconstruir_se_modelo_mudou=True, dry_run=True)
    assert indice2.limpezas == 0 and r.dry_run and r.novos == 0 and r.alterados == 2


def test_dry_run_planeja_sem_mutar_nada():
    fonte = FakeFonte({"a.md": _doc(), "b.md": _doc(titulo="Avisos RNTRC")})
    indice = FakeIndice()
    _sync(fonte, indice)

    fonte.docs["a.md"]["updated_at"] = "v2"
    fonte.docs["b.md"]["deleted"] = True
    fonte.docs["c.md"] = _doc(titulo="Novo doc")
    fonte.obter_chamados.clear()

    r = _sync(fonte, indice, dry_run=True)
    assert r.dry_run
    assert r.novos == 1 and r.alterados == 1 and r.removidos == 1
    assert fonte.obter_chamados == []            # não baixou conteúdo
    assert "c.md" not in indice.chunks           # não gravou nada
    assert not indice.controle["b.md"].deleted   # não removeu nada


def test_primeira_carga_e_incremental_vendo_tudo_como_novo():
    fonte = FakeFonte({f"d{i}.md": _doc(titulo=f"Doc {i}") for i in range(30)})
    indice = FakeIndice()
    r = _sync(fonte, indice)  # base vazia: trava não dispara, tudo entra
    assert r.novos == 30 and r.ok


def test_planejar_full_reprocessa_inalterados():
    itens = [ItemListagem(id="a", titulo="T", categoria="C", updated_at="v1")]
    estado = {"a": RegistroDoc(hash_listagem=hash_listagem(itens[0]), deleted=False)}
    assert planejar(itens, estado).inalterados == 1
    assert len(planejar(itens, estado, full=True).processar) == 1
