#!/usr/bin/env python3
"""Valida os contratos JSON de `contratos/` (só stdlib; rode com `python -I`).

Checa, para cada endpoint de `tools-infodesk/<area>/<acao>/`:

- todo `.json` é parseável (UTF-8, objeto no topo);
- existem `request.json`, `ok.json` e pelo menos um `erro-<motivo>.json`;
- todo arquivo com `status: false` tem `erro` (texto) e `motivo` (código);
- o `motivo` de cada `erro-<motivo>.json` bate com o nome do arquivo e está na
  lista de motivos daquele endpoint em `docs/plano/01-ferramentas.md`
  (tabela `MOTIVOS` abaixo);
- o conjunto de chaves de `ok.json` e de `request.json` é exatamente o
  esquema mínimo declarado em `ESQUEMA` (chaves, não valores: valores `null`
  são permitidos — ver `_notas.md` de cada endpoint).

E, para `contrato-a/`: os arquivos listados em `CONTRATO_A` existem e têm
exatamente as chaves declaradas.

Uso: `python -I contratos/validar.py` (de qualquer diretório). Sai com 1 se
houver qualquer problema; imprime um resumo por endpoint.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

RAIZ = Path(__file__).resolve().parent
TOOLS = RAIZ / "tools-infodesk"
CONTRATO_A = RAIZ / "contrato-a"

# Motivos de negócio por endpoint — espelho da coluna "Erros" de cada tool em
# docs/plano/01-ferramentas.md (seção A). Alterou o 01? Altere aqui e os JSON.
MOTIVOS: dict[str, set[str]] = {
    "contato/identificar": {"campo_invalido", "nao_identificado"},
    "ticket/preparar": {
        "campo_invalido",
        "categoria_sem_mapa",
        "chamado_aberto",
        "nao_identificado",
        "referencia_indisponivel",
    },
    "ticket/abrir": {
        "sem_proposta",
        "em_andamento",
        "infodesk_indisponivel",
        "escopo_insuficiente",
    },
    "ticket/consultar": {"nao_encontrado", "posse_nao_verificada", "infodesk_indisponivel"},
    # O 01 não lista erro para `listar`; `chamados_indisponiveis` é o motivo
    # herdado do toolsapi.go anterior para falha de leitura local — hipótese de trabalho
    # registrada em contratos/README.md (TODO(E3.1): confirmar no 01).
    "ticket/listar": {"chamados_indisponiveis"},
    "conversa/transferir": {"ja_transferida", "infodesk_indisponivel"},
}

# Esquema mínimo: chaves de primeiro nível de request.json e ok.json por
# endpoint (exatamente as do 01). Campos ainda sem valor conhecido (P3/T1)
# continuam presentes com null.
ESQUEMA: dict[str, dict[str, set[str]]] = {
    "contato/identificar": {
        "request": {"telefone", "conversation_id", "cpf", "contrato"},
        "ok": {"status", "identificado", "forma", "cliente_id", "nome_cadastro"},
    },
    "ticket/preparar": {
        "request": {
            "telefone",
            "conversation_id",
            "titulo",
            "descricao",
            "categoria",
            "prioridade",
            "identificacao",
        },
        "ok": {"status", "proposta"},
    },
    "ticket/abrir": {
        "request": {"telefone", "conversation_id", "proposta", "chave_idempotencia"},
        "ok": {"status", "numero", "mesa", "departamento", "prioridade", "transferido"},
    },
    "ticket/consultar": {
        "request": {"telefone", "conversation_id", "numero", "identificacao"},
        "ok": {"status", "numero", "status_ticket", "titulo", "aberto_em", "atualizado_em", "mesa"},
    },
    "ticket/listar": {
        "request": {"telefone", "conversation_id"},
        "ok": {"status", "total", "chamados", "observacao"},
    },
    "conversa/transferir": {
        "request": {"conversation_id", "motivo", "categoria"},
        "ok": {"status", "departamento", "mensagem_ao_cliente"},
    },
}

# Chaves obrigatórias dentro de objetos aninhados que os dois lados leem.
ANINHADO: dict[str, dict[str, set[str]]] = {
    "ticket/preparar": {
        "ok.proposta": {
            "titulo",
            "descricao",
            "categoria",
            "mesa",
            "departamento",
            "prioridade",
            "identificacao_mascarada",
            "rodape",
        },
    },
    "ticket/abrir": {
        "request.proposta": {
            "titulo",
            "descricao",
            "categoria",
            "mesa",
            "departamento",
            "prioridade",
            "identificacao_mascarada",
            "rodape",
            "criada_em",
        },
    },
}

# Extras que um erro específico carrega além de status/erro/motivo.
EXTRAS_ERRO: dict[str, dict[str, set[str]]] = {
    "ticket/preparar": {
        "chamado_aberto": {"chamados_abertos"},
        "categoria_sem_mapa": {"categoria"},
    },
}

# Contrato A (gateway → cérebro): arquivo → chaves de primeiro nível.
CONTRATO_A_ESQUEMA: dict[str, set[str]] = {
    "chat-request.json": {
        "conversation_id",
        "canal",
        "mensagem",
        "telefone",
        "nome_contato",
        "contato",
        "estado_conversa",
    },
    "chat-response.json": {"reply", "acoes", "classificacao", "fontes", "erro"},
    "chat-response-erro.json": {"reply", "acoes", "classificacao", "fontes", "erro"},
    "encerrar-episodio-request.json": {"conversation_id"},
    "encerrar-episodio-response.json": {"status", "proposta_limpa"},
    "classificar-request.json": {"conversation_id", "mensagem", "historico"},
    "classificar-response.json": {"classificacao", "frustracao", "seguranca", "esclarecimento"},
}
CLASSIFICACAO_CHAVES = {"intencao", "categoria", "prioridade", "confianca", "origem"}
ERRO_CHAT_CHAVES = {"classe", "id_ocorrencia", "causa"}


class Relatorio:
    def __init__(self) -> None:
        self.problemas: list[str] = []
        self.ok: list[str] = []

    def falha(self, msg: str) -> None:
        self.problemas.append(msg)

    def passou(self, msg: str) -> None:
        self.ok.append(msg)


def carregar(caminho: Path, rel: Relatorio) -> dict | None:
    try:
        dado = json.loads(caminho.read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        rel.falha(f"{caminho.relative_to(RAIZ)}: JSON inválido ({exc})")
        return None
    if not isinstance(dado, dict):
        rel.falha(f"{caminho.relative_to(RAIZ)}: o topo deve ser um objeto JSON")
        return None
    return dado


def checar_chaves(nome: str, dado: dict, esperado: set[str], rel: Relatorio) -> None:
    atual = set(dado)
    faltam = esperado - atual
    sobram = atual - esperado
    if faltam or sobram:
        rel.falha(
            f"{nome}: chaves diferentes do esquema (faltam={sorted(faltam)} sobram={sorted(sobram)})"
        )


def aninhado(dado: dict, trilha: str) -> dict | None:
    atual: object = dado
    for parte in trilha.split("."):
        if not isinstance(atual, dict) or parte not in atual:
            return None
        atual = atual[parte]
    return atual if isinstance(atual, dict) else None


def validar_endpoint(endpoint: str, rel: Relatorio) -> None:
    pasta = TOOLS / endpoint
    if not pasta.is_dir():
        rel.falha(f"{endpoint}: pasta ausente")
        return
    arquivos = {p.name: p for p in pasta.glob("*.json")}
    extras_na_pasta = {p.name for p in pasta.iterdir() if p.suffix not in {".json", ".md"}}
    if extras_na_pasta:
        rel.falha(f"{endpoint}: arquivos fora da convenção: {sorted(extras_na_pasta)}")

    for obrigatorio in ("request.json", "ok.json"):
        if obrigatorio not in arquivos:
            rel.falha(f"{endpoint}: falta {obrigatorio}")
    erros = sorted(n for n in arquivos if n.startswith("erro-"))
    if not erros:
        rel.falha(f"{endpoint}: precisa de pelo menos um erro-<motivo>.json")
    desconhecidos = sorted(
        n for n in arquivos if n not in {"request.json", "ok.json"} and not n.startswith("erro-")
    )
    if desconhecidos:
        rel.falha(f"{endpoint}: nomes fora da convenção request/ok/erro-<motivo>: {desconhecidos}")

    dados = {n: carregar(p, rel) for n, p in arquivos.items()}

    req = dados.get("request.json")
    if req is not None:
        checar_chaves(f"{endpoint}/request.json", req, ESQUEMA[endpoint]["request"], rel)
        if "status" in req:
            rel.falha(f"{endpoint}/request.json: request não leva `status`")

    ok = dados.get("ok.json")
    if ok is not None:
        checar_chaves(f"{endpoint}/ok.json", ok, ESQUEMA[endpoint]["ok"], rel)
        if ok.get("status") is not True:
            rel.falha(f"{endpoint}/ok.json: `status` deve ser true")

    for trilha, chaves in ANINHADO.get(endpoint, {}).items():
        arquivo, _, resto = trilha.partition(".")
        base = dados.get(f"{arquivo}.json")
        if base is None:
            continue
        obj = aninhado(base, resto)
        if obj is None:
            rel.falha(f"{endpoint}/{arquivo}.json: `{resto}` ausente ou não é objeto")
        else:
            checar_chaves(f"{endpoint}/{arquivo}.json:{resto}", obj, chaves, rel)

    motivos_vistos: set[str] = set()
    for nome in erros:
        dado = dados.get(nome)
        if dado is None:
            continue
        motivo_do_nome = nome[len("erro-") : -len(".json")]
        motivos_vistos.add(motivo_do_nome)
        if dado.get("status") is not False:
            rel.falha(f"{endpoint}/{nome}: `status` deve ser false")
        if not isinstance(dado.get("erro"), str) or not dado["erro"].strip():
            rel.falha(f"{endpoint}/{nome}: `erro` deve ser texto não vazio (pt-BR para o LLM)")
        if dado.get("motivo") != motivo_do_nome:
            rel.falha(
                f"{endpoint}/{nome}: `motivo`={dado.get('motivo')!r} não bate com o nome do arquivo"
            )
        if motivo_do_nome not in MOTIVOS[endpoint]:
            rel.falha(
                f"{endpoint}/{nome}: motivo {motivo_do_nome!r} não está na lista do 01 para este endpoint"
            )
        esperado = {"status", "erro", "motivo"} | EXTRAS_ERRO.get(endpoint, {}).get(
            motivo_do_nome, set()
        )
        checar_chaves(f"{endpoint}/{nome}", dado, esperado, rel)

    # Todo status:false (em qualquer arquivo) tem erro + motivo.
    for nome, dado in dados.items():
        if dado is not None and dado.get("status") is False:
            if "erro" not in dado or "motivo" not in dado:
                rel.falha(f"{endpoint}/{nome}: status:false exige `erro` e `motivo`")

    sem_exemplo = MOTIVOS[endpoint] - motivos_vistos
    if sem_exemplo:
        rel.falha(f"{endpoint}: motivos do 01 sem erro-<motivo>.json: {sorted(sem_exemplo)}")

    if not any(p.startswith(endpoint + ":") or p.startswith(endpoint + "/") for p in rel.problemas):
        rel.passou(
            f"{endpoint}: request, ok e {len(erros)} erro(s): {', '.join(sorted(motivos_vistos))}"
        )


def validar_contrato_a(rel: Relatorio) -> None:
    if not CONTRATO_A.is_dir():
        rel.falha("contrato-a: pasta ausente")
        return
    presentes = {p.name for p in CONTRATO_A.glob("*.json")}
    sobram = presentes - set(CONTRATO_A_ESQUEMA)
    if sobram:
        rel.falha(f"contrato-a: arquivos não declarados em CONTRATO_A_ESQUEMA: {sorted(sobram)}")
    antes = len(rel.problemas)
    for nome, chaves in CONTRATO_A_ESQUEMA.items():
        caminho = CONTRATO_A / nome
        if not caminho.is_file():
            rel.falha(f"contrato-a/{nome}: ausente")
            continue
        dado = carregar(caminho, rel)
        if dado is None:
            continue
        checar_chaves(f"contrato-a/{nome}", dado, chaves, rel)
        if nome.startswith("chat-response"):
            if dado["erro"] is None and not isinstance(dado.get("classificacao"), dict):
                rel.falha(f"contrato-a/{nome}: resposta sem erro precisa de `classificacao`")
            if isinstance(dado.get("classificacao"), dict):
                checar_chaves(
                    f"contrato-a/{nome}:classificacao",
                    dado["classificacao"],
                    CLASSIFICACAO_CHAVES,
                    rel,
                )
            if isinstance(dado.get("erro"), dict):
                checar_chaves(f"contrato-a/{nome}:erro", dado["erro"], ERRO_CHAT_CHAVES, rel)
                if dado.get("reply"):
                    rel.falha(
                        f"contrato-a/{nome}: com `erro` o `reply` é vazio (o gateway decide o texto)"
                    )
            for acao in dado.get("acoes") or []:
                if not isinstance(acao, dict) or {"tipo", "origem", "motivo"} - set(acao):
                    rel.falha(f"contrato-a/{nome}: cada ação precisa de tipo, origem e motivo")
        if nome == "classificar-response.json":
            checar_chaves(
                f"contrato-a/{nome}:classificacao", dado["classificacao"], CLASSIFICACAO_CHAVES, rel
            )
    if len(rel.problemas) == antes:
        rel.passou(f"contrato-a: {len(CONTRATO_A_ESQUEMA)} arquivos com as chaves do 01")


def main() -> int:
    rel = Relatorio()
    for endpoint in MOTIVOS:
        validar_endpoint(endpoint, rel)
    validar_contrato_a(rel)

    # Pastas de endpoint que existem mas não estão na tabela (contrato órfão).
    if TOOLS.is_dir():
        for area in TOOLS.iterdir():
            for acao in area.iterdir() if area.is_dir() else []:
                endpoint = f"{area.name}/{acao.name}"
                if endpoint not in MOTIVOS:
                    rel.falha(
                        f"{endpoint}: endpoint sem entrada em MOTIVOS/ESQUEMA (adicione ao validar.py)"
                    )

    for linha in rel.ok:
        print("ok   ", linha)
    for linha in rel.problemas:
        print("FALHA", linha)
    total = sum(1 for _ in RAIZ.rglob("*.json"))
    if rel.problemas:
        print(f"\n{len(rel.problemas)} problema(s) em {total} arquivos JSON")
        return 1
    print(f"\ncontratos ok: {total} arquivos JSON validados")
    return 0


if __name__ == "__main__":
    sys.exit(main())
