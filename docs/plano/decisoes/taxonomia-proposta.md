# Taxonomia proposta (E5.1, preparação) — o que o dono do produto precisa decidir

Estado: **proposta, nada aplicado**. `agente-ia/config/taxonomia.yaml` continua o
placeholder (`placeholder: true`); a proposta está em
`agente-ia/config/taxonomia.proposta.yaml` (mesmo formato, carrega em
`carregar_taxonomia`). A E5.1 só fecha quando as perguntas abaixo forem
respondidas e a proposta virar `taxonomia.yaml` sem `placeholder`.

Insumos: inventário do Infodesk de 2026-10-21 (nomes e ids; `docs/infodesk/09`
e `10` §2/§8), os 32 cenários de conversa reais parafraseados e anonimizados
(`agente-ia/evals/conversa/`), e a decisão de **2026-10-22: a Sofia abre todo
chamado na mesa "Abertura de Ticket"** (entrada única, triagem humana; a
classificação define prioridade e item/área, não a mesa). Esta proposta **não
decide** departamento, prioridade padrão, piso de prioridade nem item de
catálogo: o que não saiu dos dados virou pergunta.

## O que os dados dizem (fatos)

- **"Abertura de Ticket" só tem as prioridades `Alta` e `Baixa`.** Não há
  `Media` nem `Crítica` (as outras mesas têm). O `priorityId` só existe para
  dois dos quatro níveis da Sofia.
- Seu catálogo é o **mestre**: 399 itens em 17 áreas (Microsoft 365 75, Redes 49,
  Firewall 35, Servidores 35, Monitoramento 23, Computadores 21, Segurança 20,
  Backup 18, Softwares 17, Telefonia 16, Google Workspace 15, Cloud 15,
  Impressoras 14, Banco de Dados 13, Certificados Digitais 13, Projetos 10,
  Consultoria 10). É a **visão do técnico**: um cliente diz "a internet caiu", o
  catálogo tem "Sem internet", "Internet lenta", "Rede lenta", "Perda de pacotes"…
  Não há nenhum item financeiro.
- O ticket só aceita `catalogItemId` (item); **área não é campo** do ticket nem do
  formato da taxonomia. O item é opcional (`requireTicketFields` desligado,
  `10` §8 linha 2).
- Departamentos de chat existentes: `Técnicos`, `Administrativo / Financeiro`,
  `Comercial` (nenhum vínculo com mesa; a escolha é nossa, `10` §2).
- 4 nomes de item se repetem no catálogo ("Implantação Microsoft 365",
  "Conceder permissão em pasta", "Segmentação de rede", "Assessment de
  segurança"): por nome o gateway não os resolve (ambíguo). Nenhum é usado aqui.

## Categorias propostas (visão do cliente)

Nove categorias mais `indefinida`. Saíram dos 32 casos reais; categorias do
rascunho por área (24) que o cliente não distingue foram **juntas** (ex.: "internet
caiu", "internet lenta" e "oscila" são a mesma queixa para quem escreve), e áreas do
catálogo sem nenhum caso real **não** viraram categoria (ver Q1).

Todas com `mesa: Abertura de Ticket` (decidido). `catalogo` omitido em todas
(ver Q4). **Prioridade mínima: nenhuma** (ver Q3). Departamento = **proposta**
(Q2); a justificativa é a leitura do nome do departamento e do tipo do caso, não
um dado da Infominas.

| Categoria | O que o cliente diz (caso real) | Casos | Departamento proposto | Por quê | Catálogo candidato (não aplicado) |
|---|---|---|---|---|---|
| `internet_rede` | "a internet caiu e ninguém trabalha", "tá muito lenta", "cai e volta o dia todo", "dois links fora" | 11 | Técnicos | problema técnico de rede | Redes: Sem internet, Internet lenta, Rede lenta, Perda de pacotes, Wi-Fi indisponível, Wi-Fi lento |
| `vpn` | "a VPN não conecta de casa" | 1 | Técnicos | rede/segurança, atendimento técnico | Firewall: VPN não conecta, VPN conecta sem acesso à rede, VPN lenta, Usuário VPN bloqueado |
| `computador` | "computador travando e demora pra ligar", "notebook não liga" | 2 | Técnicos | estação de trabalho | Computadores: Computador lento, Computador não liga, Tela azul, Windows não inicia, Sem imagem |
| `impressora` | "impressora parou de imprimir, erro no visor" | 1 | Técnicos | periférico | Impressoras: Não imprime, Impressora offline, Scanner não funciona, Impressão com falhas |
| `email` | "não consigo enviar e-mail, fica na caixa de saída" | 1 | Técnicos | e-mail corporativo | Microsoft 365: Não envia e-mails, Não recebe e-mails, Outlook não abre; Google Workspace: Gmail não envia |
| `servidor_arquivos` | "servidor reiniciando sozinho", "pasta compartilhada lenta" | 2 | Técnicos | infraestrutura | Servidores: Servidor indisponível, Compartilhamento inacessível, Espaço em disco crítico |
| `acesso_senha` | "acesso da colega bloqueado, pede senha que ela não lembra" | 1 | Técnicos | desbloqueio/reset pela equipe técnica | sem área inequívoca (AD em Servidores; Google em Google Workspace; Microsoft 365) |
| `financeiro` | "veio cobrança duas vezes", "preciso do boleto desse mês" | 2 | Administrativo / Financeiro | é o departamento do assunto; "Técnicos" não trata cobrança | nenhum (o catálogo não tem item financeiro) |
| `comercial` | "quanto fica montar a rede de uma loja nova com 12 pontos" | 1 | Comercial | único departamento de proposta/preço | Projetos: Cabeamento estruturado; Redes: Novo ponto de rede; Consultoria: Assessment de TI |
| `indefinida` | "oi, preciso de ajuda", "[áudio]", "[imagem] olha o erro" | 3 | Técnicos (= padrão proposto) | sem assunto ainda; a triagem humana é a mesma mesa | — |

"Casos" conta cenários (de 32) cujo assunto cai na categoria (os de e-mail, VPN e acesso estão hoje como `indefinida` nos cenários, porque o placeholder não tem a categoria); os outros 10 são
pedidos de atendente, status de chamado, fora de escopo, injeção e pedido de senha de terceiro, que não têm categoria.
A coluna de catálogo é só consulta para a Q4: nenhum item foi aplicado porque
cada categoria agrupa vários itens.

`mesa_triagem` = `Abertura de Ticket` (mesma mesa; decidido). `departamento_humano_padrao` =
`Técnicos` (proposta, Q2).

### Prioridades (bloco `prioridades` da proposta)

| Nome da Sofia | Nome em "Abertura de Ticket" | Situação |
|---|---|---|
| `baixa` | `Baixa` | existe |
| `media` | — (`null`: não enviar `priorityId`) | **decisão pendente (Q3)**: a mesa não tem `Media` |
| `alta` | `Alta` | existe |
| `urgente` | `Alta` | **decisão pendente (Q3)**: a mesa não tem `Crítica`; proposta = a maior que existe |

## Decisões pendentes

Cada pergunta traz opções e a recomendação. Marcas "DECISÃO PENDENTE" no YAML
apontam para elas.

### Q1 — As categorias estão certas?

Aprovar, ajustar ou ampliar a lista de nove categorias.

| Opção | Efeito |
|---|---|
| **A. Aprovar como está (recomendada)** | cobre todos os 32 casos reais; assunto fora da lista cai em `indefinida` → a Sofia esclarece uma vez e passa a um atendente |
| B. Ajustar | por exemplo separar `internet_rede` em "sem internet" e "internet lenta" (só vale se Q3/Q4 precisarem — hoje o cliente não os distingue), ou juntar `vpn` em `internet_rede` |
| C. Ampliar | criar categorias para áreas do catálogo sem caso real nos 32: Backup, Segurança (vírus, phishing), Telefonia (ramal), Softwares/ERP, Certificados Digitais, Banco de Dados, Cloud, Microsoft 365 além de e-mail |

Recomendação: **A agora, C só com evidência**. Sem uma frase real de cliente os
exemplos seriam inventados (regra 6). A evidência vem de contar assuntos do histórico
(`analise-infodesk`, E5.2). Perguntas junto: as categorias do sintético antigo
(`cancelamento`, `mudanca_endereco`, `visita_tecnica`) existem para a Infominas? Nenhum dos 32
casos fala delas; ficam fora (o sintético as manda para `indefinida`).

Destrava: todos os cenários que usam categoria (lista abaixo).

### Q2 — Departamento do handoff: por categoria e o padrão

Duas respostas: (a) a coluna "Departamento proposto" acima; (b) o
`departamento_humano_padrao` (usado quando a categoria é `indefinida` ou desconhecida).

| Opção para o padrão | Efeito |
|---|---|
| **`Técnicos` (recomendada)** | é o que `10` §8 já propunha; a maioria dos casos é técnica |
| `Administrativo / Financeiro` ou `Comercial` | só faz sentido se a Infominas quiser a recepção por lá |

Perguntas que o dono precisa fechar junto: (1) cada um dos três departamentos tem atendentes
ativos hoje? (`GET /users`, `departmentIds`; `10` §4: sem `userId` a conversa entra
na fila `PENDING` e quem é notificado são os atendentes do departamento); (2) o
horário humano (P7) — nenhum dos 32 cenários depende dele.

**Limitação de produto (fora desta etapa):** hoje a categoria só é fixada quando a intenção é
`reclamacao_problema` (`INTENCOES_COM_CATEGORIA`, `src/classificacao/tipos.py`). Um pedido de boleto
ou de preço (intenções `outro`/`duvida_informacao`) chega à transferência com categoria `indefinida`
e vai para o departamento **padrão**, não para o da categoria. Responder Q2 não basta para
`financeiro` e `comercial` irem aos departamentos próprios; precisa de uma mudança em `src/` (candidata
a etapa nova). Por isso os dois cenários correspondentes continuam `xfail`.

Destrava: `real-assunto-comercial-planos`, `real-financeiro-segunda-via` (com a mudança de produto).

### Q3 — Prioridades: o que fazer sem `Media` e `Crítica`

| Sub-decisão | Opções | Recomendação |
|---|---|---|
| `media` (também é a prioridade padrão quando o JEV tem pouca confiança, `02`) | (i) não enviar `priorityId`: o ticket nasce na prioridade padrão da mesa e a triagem escolhe; (ii) mapear para `Baixa`; (iii) mapear para `Alta`; (iv) pedir à Infominas que crie `Media` na mesa | **(i)**: não atribui à força uma urgência que a mesa não tem; é o que o gateway já faz com `null` |
| `urgente` | `Alta` (proposta) ou não enviar | `Alta`: a maior que existe; assim "empresa parada" não perde a marca de urgência |
| Piso por categoria (`prioridade_minima`) | nenhum; ou `alta` em `internet_rede` e `servidor_arquivos` | **nenhum**: `internet_rede` junta "caiu" e "lenta", e um piso `alta` subiria todo chamado de lentidão. O Score do JEV decide; só se o dono quiser regra fixa para "tudo parado" a categoria precisa ser separada (Q1-B) |

Nada nos dados dá motivo explícito para um piso, então a proposta não traz nenhum.

Destrava: nenhum dos 32 cenários afirma prioridade. Afeta o `priorityId` do ticket e os cenários
da raiz que testam piso (`piso-de-prioridade-vence-o-jev`), que hoje usam o piso do placeholder.

### Q4 — Item de catálogo (`catalogItemId`): omitir, usar item ou usar área?

A decisão de 22/10 diz que a classificação define "item/área". O formato da taxonomia só
tem `catalogo` (nome de **um item**); o ticket só aceita item.

| Opção | Efeito |
|---|---|
| **A. Omitir `catalogo` (recomendada)** | a triagem humana escolhe o item; é o que a proposta faz; nenhuma categoria agrupa um item só |
| B. Item só onde a categoria equivale a um item | exige categorias mais finas (Q1-B), por exemplo `vpn_nao_conecta` ↔ item "VPN não conecta"; mais classes para o JEV acertar |
| C. Criar o campo `area` no formato | dá a área (ex.: Redes) mas **não** chega ao ticket, que não tem área; só serviria para texto/auditoria; exige mudança em `taxonomia.py`/`taxonomia.go` |

Recomendação: **A**; reavaliar B quando houver dados de uso por item (E5.2).

### Q5 — Cadastro de telefone desconhecido e CPF (E5.3, não é da taxonomia)

Dois cenários assumem o fluxo atual (telefone sem cadastro → pede CPF). A decisão de 22/10 de
cadastrar o solicitante via `POST /requesters` muda esse fluxo; "telefone cadastrado dispensa CPF?"
segue em aberto em `07` N3. A sessão da E5.3 resolve; aqui só ficam parados.

Destrava: `real-nao-identificado-informa-cpf`, `real-cpf-invalido`.

## Cenários reais: o que foi promovido e o que cada decisão destrava

Dez cenários **independem de categoria** (a categoria é `indefinida` ou nem há) e passam
tanto na taxonomia em uso quanto na proposta: foram movidos (`git mv`) de
`evals/conversa/pendentes/` para `evals/conversa/` e entram no CI (`pytest -m evals_pr`):

`real-cliente-manda-audio`, `real-cliente-manda-foto`, `real-fora-de-escopo`,
`real-injecao-de-prompt`, `real-mensagem-vaga`, `real-pede-atendente`,
`real-pede-pessoa-especifica`, `real-pede-senha-de-terceiro`, `real-status-com-numero`,
`real-status-sem-numero`.

Os outros 22 ficam em `pendentes/`, **já remapeados** para as categorias da proposta
(`test_taxonomia_proposta.py` os executa contra ela; todos passam, os dois `xfail`
continuam `xfail`). Não podem ir para a raiz antes da aprovação porque o runner valida a
categoria contra `taxonomia.yaml`: `internet_rede` ainda não existe lá.

| Destrava | Cenários |
|---|---|
| **Q1** (aprovar categorias e promover a proposta a `taxonomia.yaml`; depois `git mv`) | `real-internet-caiu-empresa`, `real-internet-lenta`, `real-internet-oscila`, `real-dois-links-fora-urgente`, `real-formulario-do-site`, `real-cita-protocolo-nao-abre-novo`, `real-cliente-irritado`, `real-cliente-impaciente-sem-transferir`, `real-vago-depois-descreve` (`internet_rede`); `real-computador-lento`, `real-pergunta-nao-confirma` (`computador`); `real-impressora-nao-imprime` (`impressora`); `real-servidor-desligando`, `real-muda-descricao-antes-de-confirmar` (`servidor_arquivos`); `real-cobranca-financeiro` (`financeiro`) |
| **Q1**, reescrevendo o esperado (hoje descrevem o comportamento sem categoria: esclarece e transfere; com a categoria, deveria abrir chamado) | `real-email-nao-envia` (`email`), `real-vpn-nao-conecta` (`vpn`), `real-senha-acesso-usuario` (`acesso_senha`) |
| **Q1 + Q2** e a mudança de produto descrita em Q2 | `real-assunto-comercial-planos` (`comercial`), `real-financeiro-segunda-via` (`financeiro`) |
| **Q5** (E5.3) | `real-nao-identificado-informa-cpf`, `real-cpf-invalido` |

Cada arquivo em `pendentes/` traz uma linha `# Destrava: …` com a mesma informação.

## O que muda quando a proposta virar `taxonomia.yaml` (aviso para o fechamento da E5.1)

Não faz parte desta preparação; é o custo de trocar as categorias:

1. **31 cenários da raiz e o `TEMPLATE.yaml`** usam categorias do placeholder (`sem_conexao`,
   `lentidao`, `cobranca`…), assim como 13 arquivos de `tests/` (`test_classificacao_*`, `test_graph*`, `test_server*`,
   `test_acao_sem_ferramenta`, `test_app`, `fakes.py`). Precisam ser remapeados no mesmo PR.
2. `evals/classificacao/sintetico.jsonl` foi escrito para o placeholder (provedor de
   internet); `python -m evals.classificacao --fonte sintetico --jev` (critério da E5.1)
   só tem sentido depois de reescrevê-lo para estas categorias (zona da E2.4).
   `tests/test_taxonomia_proposta.py` guarda o destino de cada categoria antiga.
3. `scripts/dev/taxonomia.dev.yaml` (mock) acompanha as categorias novas.
4. Rodar o gateway contra o Infodesk real: o boot resolve mesa, prioridade e departamento por nome
   exato; `tests/fixtures/infodesk_referencia.json` já prova que os nomes da proposta existem (só
   nomes e ids, sem dado de pessoa).

## Como conferir

```bash
cd agente-ia
pytest tests/test_taxonomia_proposta.py -v     # formato, nomes contra o Infodesk, decisões marcadas, 32 cenários
pytest -m evals_pr evals/                      # CI de hoje, com os 10 promovidos
```
