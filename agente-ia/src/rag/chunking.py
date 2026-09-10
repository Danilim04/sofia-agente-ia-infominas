"""Chunking de um `DocumentoFonte` — mesmo fatiamento da ingestão local.

Reaproveita o pipeline de `src.rag.ingest` (seções por cabeçalho Markdown →
chunks com overlap) para que um documento vindo da API do cliente produza
chunks idênticos aos que os `docs/*.md` produzem hoje. Contrato de metadados
(consumido por `buscar_chunks` e pela validação de citações do
`output_guardrail_node` — NÃO renomear):

- `source`: rótulo citável = `titulo` do documento;
- `secao`: caminho de cabeçalhos ("Módulo X › 4.3 Botões");
- `categoria` e `doc_id`: rastreabilidade/filtros do índice.
"""

from __future__ import annotations

from langchain_core.documents import Document
from langchain_text_splitters import MarkdownHeaderTextSplitter

from src.rag.fonte import DocumentoFonte
from src.rag.ingest import _HEADERS, _secao_de, split_documents


def chunkear_documento(
    doc: DocumentoFonte,
    chunk_size: int = 800,
    chunk_overlap: int = 120,
) -> list[Document]:
    """Documento completo → chunks com metadados citáveis. Vazio → []."""
    texto = (doc.conteudo or "").strip()
    if not texto:
        return []

    splitter = MarkdownHeaderTextSplitter(
        headers_to_split_on=_HEADERS, strip_headers=False
    )
    secoes: list[Document] = []
    for secao in splitter.split_text(texto):
        secao.metadata = {
            "source": doc.titulo,
            "secao": _secao_de(secao.metadata),
            "categoria": doc.categoria,
            "doc_id": doc.id,
        }
        secoes.append(secao)
    return split_documents(secoes, chunk_size, chunk_overlap)
