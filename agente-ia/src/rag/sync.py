"""Sincronização incremental da base de conhecimento (Contrato B).

Motor do ciclo diário: listagem da fonte → plano (o que processar/remover) →
execução com transação por documento no índice. Decisões de projeto (acordadas
no Contrato B e registradas aqui para não regredirem):

- **Sinal de mudança = hash dos METADADOS da listagem** (`titulo + categoria +
  updated_at + deleted`), calculado sem baixar conteúdo. `updated_at` fica
  DENTRO do hash por decisão explícita: preferimos reprocessar à toa (ms de
  CPU local) a arriscar perder uma mudança real. Não reintroduzir comparação
  de conteúdo no ciclo diário — se o `updated_at` mentir, a correção é na
  origem (Contrato B §4.1), não heurística aqui.
- **Deleção só por tombstone** (`deleted=true`). Documento AUSENTE da listagem
  é anomalia logada, nunca deleção — um filtro bugado na fonte não pode
  destruir o índice. Exceção explícita: `remover_ausentes=True` (fonte local,
  onde a listagem é um glob de diretório e não tem tombstone).
- **Trava anti-remoção em massa**: mais remoções que `max(2, limiar × base)`
  num ciclo aborta sem deletar nada (libera com `permitir_remocao_em_massa`
  após confirmação humana). Listagem vazia com base não-vazia idem.
- **Idempotente/fail-soft por documento**: falha em um doc conta em `falhas`
  e não interrompe os demais; a reexecução converge (o hash só é registrado
  junto com os chunks, na mesma transação).

CLI: `python -m src.rag.sync [--fonte local|api] [--full] [--dry-run]
[--remover-ausentes] [--permitir-remocao-em-massa]` — exit 0 só com ciclo
100% limpo (cron alerta em exit != 0). Com `--fonte api` o ciclo começa pelo
preflight `GET /me` (chave/escopos/espaços) e só depois toca no índice.
"""

from __future__ import annotations

import argparse
import hashlib
import logging
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Optional, Protocol

from src.rag.fonte import (
    DocumentoFonte,
    ErroDeFonte,
    FonteDocumentacao,
    ItemListagem,
    RegistroDoc,
)

if TYPE_CHECKING:
    from langchain_core.documents import Document
    from langchain_core.embeddings import Embeddings

logger = logging.getLogger(__name__)


class TravaRemocaoMassa(RuntimeError):
    """Remoções acima do limiar num ciclo — exige confirmação humana."""


class ErroModeloIncompativel(RuntimeError):
    """Modelo de embeddings difere do registrado no índice — exige --full."""


class IndiceVetorial(Protocol):
    """O que o sync precisa de um índice (PgIndice em prod; fake nos testes)."""

    def preparar_esquema(self, embedding_model: str, dim: int) -> None: ...

    def modelo_registrado(self) -> Optional[str]: ...

    def limpar_tudo(self, embedding_model: str, dim: int) -> None: ...

    def estado(self) -> dict[str, RegistroDoc]: ...

    def substituir_documento(
        self,
        item: ItemListagem,
        hash_listagem: str,
        chunks: "list[Document]",
        vetores: list[list[float]],
    ) -> None: ...

    def remover_documento(self, doc_id: str, hash_listagem: str) -> None: ...


def hash_listagem(item: ItemListagem) -> str:
    """Hash determinístico dos metadados da listagem (o sinal de mudança)."""
    base = "\x1f".join(
        [item.titulo, item.categoria, item.updated_at, "1" if item.deleted else "0"]
    )
    return hashlib.sha256(base.encode("utf-8")).hexdigest()


@dataclass
class PlanoSync:
    processar: list[ItemListagem] = field(default_factory=list)
    remover: list[ItemListagem] = field(default_factory=list)  # tombstones/ausentes
    inalterados: int = 0
    tombstones_ignorados: int = 0  # já removidos ou nunca indexados
    ausentes: list[str] = field(default_factory=list)  # anomalia (sem tombstone)


@dataclass
class ResultadoSync:
    novos: int = 0
    alterados: int = 0
    removidos: int = 0
    inalterados: int = 0
    falhas: int = 0
    ausentes: int = 0
    dry_run: bool = False

    @property
    def ok(self) -> bool:
        return self.falhas == 0


def planejar(
    itens: list[ItemListagem],
    estado: dict[str, RegistroDoc],
    *,
    full: bool = False,
    remover_ausentes: bool = False,
) -> PlanoSync:
    """Classifica cada item da listagem contra o estado registrado no índice."""
    plano = PlanoSync()
    ids_listados = set()

    for item in itens:
        ids_listados.add(item.id)
        registro = estado.get(item.id)
        if item.deleted:
            if registro is not None and not registro.deleted:
                plano.remover.append(item)
            else:
                plano.tombstones_ignorados += 1
            continue
        mudou = (
            registro is None
            or registro.deleted  # restaurado
            or registro.hash_listagem != hash_listagem(item)
        )
        if full or mudou:
            plano.processar.append(item)
        else:
            plano.inalterados += 1

    vivos_nao_listados = [
        doc_id
        for doc_id, reg in estado.items()
        if not reg.deleted and doc_id not in ids_listados
    ]
    if remover_ausentes:
        for doc_id in vivos_nao_listados:
            plano.remover.append(
                ItemListagem(
                    id=doc_id, titulo="", categoria="", updated_at="", deleted=True
                )
            )
    else:
        plano.ausentes = vivos_nao_listados

    return plano


def _checar_travas(
    itens: list[ItemListagem],
    estado: dict[str, RegistroDoc],
    plano: PlanoSync,
    limiar_remocao: float,
    permitir_remocao_em_massa: bool,
) -> None:
    """Travas anti-catástrofe — rodam ANTES de qualquer mutação no índice."""
    base_viva = sum(1 for reg in estado.values() if not reg.deleted)
    if base_viva == 0:
        return  # primeira carga: nada a proteger
    if not itens:
        raise TravaRemocaoMassa(
            f"listagem vazia com base não-vazia ({base_viva} docs) — "
            "indistinguível de falha grave da fonte; nada foi alterado"
        )
    teto = max(2, int(limiar_remocao * base_viva))
    if len(plano.remover) > teto and not permitir_remocao_em_massa:
        raise TravaRemocaoMassa(
            f"{len(plano.remover)} remoções num ciclo (teto: {teto} para base de "
            f"{base_viva}) — reexecute com --permitir-remocao-em-massa após "
            "confirmação humana; nada foi alterado"
        )


def executar_sync(
    fonte: FonteDocumentacao,
    indice: IndiceVetorial,
    embeddings: "Embeddings",
    *,
    embedding_model: str,
    chunk_size: int = 800,
    chunk_overlap: int = 120,
    full: bool = False,
    remover_ausentes: bool = False,
    limiar_remocao: float = 0.2,
    permitir_remocao_em_massa: bool = False,
    dry_run: bool = False,
    reconstruir_se_modelo_mudou: bool = False,
) -> ResultadoSync:
    """Executa um ciclo de sincronização. Levanta exceção quando aborta.

    Exceções (nada foi alterado no índice quando levantadas antes da fase de
    execução): `ErroDeFonte` (fonte fora do contrato), `TravaRemocaoMassa`,
    `ErroModeloIncompativel`.
    """
    itens = fonte.listar()  # ErroDeFonte propaga → ciclo aborta intacto

    dim = len(embeddings.embed_query("dimensao"))
    indice.preparar_esquema(embedding_model, dim)

    registrado = indice.modelo_registrado()
    if registrado is not None and registrado != embedding_model:
        if not full and not reconstruir_se_modelo_mudou:
            raise ErroModeloIncompativel(
                f"índice foi gerado com '{registrado}', config atual é "
                f"'{embedding_model}' — misturar espaços vetoriais degrada a "
                "busca silenciosamente; rode com --full para reconstruir"
            )
        # Troca de modelo assumida (deploy com --reconstruir-se-modelo-mudou ou
        # --full): o índice inteiro é refeito no novo espaço vetorial.
        logger.warning(
            "sync_modelo_trocado de=%s para=%s — reconstruindo o índice inteiro",
            registrado, embedding_model,
        )
        full = True
        if not dry_run:
            indice.limpar_tudo(embedding_model, dim)

    estado = indice.estado()
    plano = planejar(itens, estado, full=full, remover_ausentes=remover_ausentes)
    _checar_travas(itens, estado, plano, limiar_remocao, permitir_remocao_em_massa)

    resultado = ResultadoSync(inalterados=plano.inalterados, dry_run=dry_run)
    resultado.ausentes = len(plano.ausentes)
    if plano.ausentes:
        logger.warning(
            "sync_anomalia %d doc(s) vivos ausentes da listagem SEM tombstone "
            "(nada removido — Contrato B §5 cenário de anomalia): %s",
            len(plano.ausentes),
            plano.ausentes[:10],
        )

    if dry_run:
        resultado.novos = sum(1 for i in plano.processar if i.id not in estado)
        resultado.alterados = len(plano.processar) - resultado.novos
        resultado.removidos = len(plano.remover)
        _log_resumo(resultado)
        return resultado

    for item in plano.processar:
        era_novo = item.id not in estado
        try:
            _processar_documento(fonte, indice, embeddings, item, chunk_size, chunk_overlap)
        except Exception as exc:  # noqa: BLE001 — fail-soft por doc; reexecução converge
            logger.error("sync_doc_falhou id=%s erro=%s", item.id, exc)
            resultado.falhas += 1
            continue
        if era_novo:
            resultado.novos += 1
        else:
            resultado.alterados += 1

    for item in plano.remover:
        try:
            indice.remover_documento(item.id, hash_listagem(item))
        except Exception as exc:  # noqa: BLE001
            logger.error("sync_remocao_falhou id=%s erro=%s", item.id, exc)
            resultado.falhas += 1
            continue
        resultado.removidos += 1

    _log_resumo(resultado)
    return resultado


def _processar_documento(
    fonte: FonteDocumentacao,
    indice: IndiceVetorial,
    embeddings: "Embeddings",
    item: ItemListagem,
    chunk_size: int,
    chunk_overlap: int,
) -> None:
    from src.rag.chunking import chunkear_documento

    doc: DocumentoFonte = fonte.obter(item.id)
    chunks = chunkear_documento(doc, chunk_size, chunk_overlap)
    if not chunks:
        # Documento sem conteúdo útil: mantém a versão indexada (se houver) e
        # conta como falha — sumir com conhecimento exige tombstone, não vazio.
        raise ErroDeFonte(f"documento {item.id} veio sem conteúdo útil")
    vetores = embeddings.embed_documents([c.page_content for c in chunks])
    indice.substituir_documento(item, hash_listagem(item), chunks, vetores)
    logger.info("sync_doc_ok id=%s chunks=%d", item.id, len(chunks))


def _log_resumo(r: ResultadoSync) -> None:
    logger.info(
        "sync_resumo novos=%d alterados=%d removidos=%d inalterados=%d "
        "falhas=%d ausentes=%d dry_run=%s",
        r.novos, r.alterados, r.removidos, r.inalterados, r.falhas, r.ausentes, r.dry_run,
    )


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def _montar_fonte(nome: str, args: argparse.Namespace) -> FonteDocumentacao:
    from src.config import get_settings

    settings = get_settings()
    if nome == "local":
        from src.rag.fonte import FonteDocsLocais

        return FonteDocsLocais(args.docs_dir or settings.docs_dir)
    if nome == "api":
        from src.rag.fonte import FonteAPIDocumentacao

        if not settings.docs_api_base_url:
            raise SystemExit("DOCS_API_BASE_URL não configurada no .env (fonte=api).")
        if not settings.docs_api_key:
            raise SystemExit(
                "DOCS_API_KEY não configurada no .env (fonte=api) — chave azk_... "
                "gerada no AzapDocs, com os escopos DOCS_LIST e DOCS_READ."
            )
        return FonteAPIDocumentacao(
            base_url=settings.docs_api_base_url,
            api_key=settings.docs_api_key,
            timeout=settings.docs_api_timeout,
        )
    raise SystemExit(f"fonte desconhecida: {nome}")


def _preflight_api(fonte: FonteDocumentacao) -> None:
    """`GET /me` ANTES de tocar no índice: a API responde qual das três condições
    caiu (chave, API do espaço, acesso do dono) — o erro fica legível no log do
    cron em vez de um 401/listagem vazia no meio do ciclo."""
    try:
        diag = fonte.diagnosticar()  # type: ignore[attr-defined]
    except ErroDeFonte as exc:
        logger.error("sync_abortado motivo=diagnóstico da API falhou (%s)", exc)
        raise SystemExit(1) from exc
    for esp in diag.espacos:
        logger.info(
            "docs_api_espaco nome=%r utilizavel=%s%s",
            esp.nome, esp.utilizavel,
            "" if esp.utilizavel else f" motivo={esp.motivo_bloqueio!r}",
        )
    if not diag.pronto:
        logger.error("sync_abortado motivo=API sem espaço utilizável: %s",
                     "; ".join(diag.problemas()))
        raise SystemExit(1)
    logger.info(
        "docs_api_ok usuario=%r espacos_utilizaveis=%d contrato=%s retencao_tombstone_dias=%s",
        diag.usuario, sum(1 for e in diag.espacos if e.utilizavel),
        diag.contrato_versao, diag.retencao_tombstone_dias,
    )


def _cli() -> None:
    from src.config import get_settings
    from src.rag.retriever import get_embeddings

    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s"
    )
    settings = get_settings()

    parser = argparse.ArgumentParser(
        description="Sincroniza a base de conhecimento no índice pgvector (Contrato B)."
    )
    parser.add_argument("--fonte", choices=["local", "api"], default="local")
    from pathlib import Path as _Path

    parser.add_argument("--docs-dir", type=_Path, default=None,
                        help="Diretório dos .md (só fonte=local; default: DOCS_DIR).")
    parser.add_argument("--full", action="store_true",
                        help="Reprocessa tudo (primeira carga, troca de modelo/chunking).")
    parser.add_argument("--dry-run", action="store_true",
                        help="Mostra o plano (novos/alterados/removidos) sem gravar nada.")
    parser.add_argument("--remover-ausentes", action="store_true",
                        help="Trata ausência na listagem como remoção (só fonte local).")
    parser.add_argument("--permitir-remocao-em-massa", action="store_true",
                        help="Libera um ciclo com remoções acima do limiar (confirmação humana).")
    parser.add_argument("--reconstruir-se-modelo-mudou", action="store_true",
                        help="Se EMBEDDINGS_MODEL difere do registrado no índice, refaz tudo "
                             "(em vez de abortar). Usado pelo deploy; o cron segue estrito.")
    args = parser.parse_args()

    if not settings.pgvector_url:
        raise SystemExit("PGVECTOR_URL não configurada no .env — o sync grava no pgvector.")

    from src.rag.indice_pg import PgIndice

    fonte = _montar_fonte(args.fonte, args)
    if args.fonte == "api":
        _preflight_api(fonte)
    indice = PgIndice.conectar(settings.pgvector_url)
    embeddings = get_embeddings()
    logger.info("sync_embeddings provider=%s modelo=%s",
                settings.embeddings_provider, settings.embeddings_model)

    try:
        resultado = executar_sync(
            fonte,
            indice,
            embeddings,
            embedding_model=settings.embeddings_model,
            chunk_size=settings.rag_chunk_size,
            chunk_overlap=settings.rag_chunk_overlap,
            full=args.full,
            remover_ausentes=args.remover_ausentes,
            limiar_remocao=settings.sync_limiar_remocao,
            permitir_remocao_em_massa=args.permitir_remocao_em_massa,
            dry_run=args.dry_run,
            reconstruir_se_modelo_mudou=args.reconstruir_se_modelo_mudou,
        )
    except (ErroDeFonte, TravaRemocaoMassa, ErroModeloIncompativel) as exc:
        logger.error("sync_abortado motivo=%s", exc)
        raise SystemExit(1) from exc
    finally:
        indice.fechar()

    print(
        f"Sync {'(dry-run) ' if resultado.dry_run else ''}concluído: "
        f"{resultado.novos} novos, {resultado.alterados} alterados, "
        f"{resultado.removidos} removidos, {resultado.inalterados} inalterados, "
        f"{resultado.falhas} falhas, {resultado.ausentes} ausentes."
    )
    if not resultado.ok:
        raise SystemExit(1)


if __name__ == "__main__":
    _cli()
