"""Tools do agente.

Nenhum import no nível do pacote: `rag_tool` puxa o retriever (embeddings,
índice) e as tools do Infodesk (E2.3) puxam `httpx`/config — quem precisa
importa o módulo concreto tardiamente (`from src.tools.rag_tool import
buscar_chunks`). Assim `import src.tools.efeitos` continua barato.
"""
