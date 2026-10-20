#!/usr/bin/env python3
"""Summarize preserved QA-009 evidence; reject unexplained mutation outcomes."""
import collections
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
EVIDENCE = ROOT / "docs/qa/defect-elimination/evidence"
REPORT = EVIDENCE.parent
CAMPAIGNS = {
    "player": "mutation-player-final",
    "fidelity": "mutation-fidelity-reviewed",
    "storage": "mutation-storage-final",
    "permissions": "mutation-permissions-reviewed",
}
EQUIVALENT = {
    ("controls.go", 34, 7): "Move(i,i): os dois ramos copiam slices vazios e preservam a mesma entrada.",
    ("queue.go", 67, 81): "IDs dos imports são distintos: progress.ID é a chave única de p.jobs; < e <= ordenam igualmente registros distintos. Replay isolado também passou.",
    ("spotify_rank.go", 211, 97): "Com artists vazio, o bloco anterior já atribui Artist=1 à mesma identidade de canal; com artists não vazio, a conjunção permanece falsa. Alterar t.Artist != vazio não muda o resultado.",
}
BEHAVIOR = {
    "player": {141: "TestHardeningSeekValidationPreservesPlayback", 67: "TestHardeningConcurrentImportSnapshotOrder + comparator-independent.txt"},
    "storage": {152: "TestHardeningSettingsReadDistinguishesAbsentAndUnavailable", 155: "TestHardeningSettingsReadDistinguishesAbsentAndUnavailable", 180: "TestHardeningLegacyMappingShapeIsNotCacheHit"},
    "permissions": {123: "TestHardeningAuthorizedCommandOutcomes", 167: "TestHardeningVoiceJoinAndAttachFailures", 176: "TestHardeningVoiceJoinAndAttachFailures", 220: "TestHardeningAuthorizedCommandOutcomes", 244: "TestHardeningPlayFailureResponseAndCancellation", 246: "TestHardeningPlayFailureResponseAndCancellation", 301: "TestHardeningProgressiveFirstPositionNotice", 304: "TestHardeningProgressiveFirstPositionNotice", 310: "TestHardeningYouTubeCollectionSkippedNotice", 340: "TestHardeningAuthorizedCommandOutcomes", 361: "TestHardeningAuthorizedCommandOutcomes"},
}

def behavior(campaign, line, column):
    if campaign != "fidelity":
        return BEHAVIOR[campaign][line]
    if line in (99, 190, 255):
        return "TestHardeningSpotifyIdentityEvidence"
    if line == 186:
        return "TestHardeningUnrelatedChannelCannotSupplyPrimaryArtist" if column == 45 else "TestHardeningSpotifyIdentityEvidence"
    if line in (275, 282):
        return "TestHardeningDiscoveryDeduplicationPreservesRicherIdentity"
    if line == 412:
        return "TestHardeningVerificationInfrastructureErrorsRemainActionable"
    if line == 347 and column == 11:
        return "TestHardeningDiscoveryPreservesExtractorFailure"
    return "TestHardeningMappingTrustAndFailureRecovery"

mutation_rows = []
original_rows = []
for campaign, directory in CAMPAIGNS.items():
    data = json.loads((EVIDENCE / directory / (campaign + ".json")).read_text())
    metadata = json.loads((EVIDENCE / directory / (campaign + "-run.json")).read_text())
    assert metadata["exit_code"] == 0 and not metadata.get("infrastructure_failures")
    index = {}
    for source in data["files"]:
        for mutant in source["mutations"]:
            key = (source["file_name"], mutant["line"], mutant["column"])
            assert mutant["status"] == "KILLED" or (mutant["status"] == "LIVED" and key in EQUIVALENT), key
            index[key] = mutant
            mutation_rows.append(dict(campaign=campaign, file=source["file_name"], **mutant,
                classification="Equivalente" if mutant["status"] == "LIVED" else "Detectado",
                rationale=EQUIVALENT.get(key), evidence=directory + "/" + campaign + "-executions.jsonl"))
    baseline = json.loads((ROOT / "docs/qa/advanced/evidence/mutation" / (campaign + ".json")).read_text())
    for source in baseline["files"]:
        for mutant in source["mutations"]:
            if mutant["status"] != "NOT COVERED":
                continue
            line = mutant["line"] + (4 if source["file_name"] == "spotify_rank.go" and mutant["line"] >= 115 else 0)
            current = index[(source["file_name"], line, mutant["column"])]
            original_rows.append(dict(campaign=campaign, file=source["file_name"],
                old_line=mutant["line"], column=mutant["column"], current_line=line,
                status=current["status"], classification="Equivalente" if current["status"] == "LIVED" else "Teste insuficiente (corrigido)",
                protection=behavior(campaign, mutant["line"], mutant["column"])))
assert len(original_rows) == 43
(EVIDENCE / "mutation-review.json").write_text(json.dumps(dict(all=mutation_rows, original_43=original_rows), indent=2) + "\n")
text = "# QA-009 — revisão de mutação\n\n"
text += "O score QA-008 foi retirado: `--test-cpu=2` faz Gremlins 0.6.0 gerar um argumento único `-cpu 2`; go test falha sem executar a suíte e o executor conta exit=1 como KILLED. A reprodução está em evidence/gremlins-test-cpu-invalid.txt. As campanhas mutation/ e mutation-final/ ainda têm essa configuração e **não são evidência de detecção**.\n\n"
text += "Configuração corrigida: GOMAXPROCS=2, sem --test-cpu. Cada execução preserva comando, diff do mutante e saída em JSONL. Nenhuma compilação inválida, setup failure ou timeout foi contabilizado como detectado no resultado final. O runner retorna 1 quando existe LIVED, mesmo se posteriormente revisado como equivalente; essa falha operacional é preservada, não convertida em exit=0.\n\n"
text += "| Campanha final | Detectados | Sobreviventes equivalentes | Não cobertos / inválidos / timeout |\n|---|---:|---:|---:|\n"
for campaign, directory in CAMPAIGNS.items():
    rows = [m for m in mutation_rows if m["campaign"] == campaign]
    text += f"| [{campaign}](evidence/{directory}/{campaign}.txt) | {sum(m['status']=='KILLED' for m in rows)} | {sum(m['status']=='LIVED' for m in rows)} | 0 |\n"
killed = sum(m["status"] == "KILLED" for m in mutation_rows)
text += f"\nTotal: {killed}/{len(mutation_rows)} detectados ({100*killed/len(mutation_rows):.2f}%, score bruto); três equivalentes explicitamente revisados. Score ajustado: 218/218, apenas neste escopo de arquivos e operadores. Isto não demonstra ausência de defeitos nem mede todas as invariantes da aplicação.\n\n"
text += "## Equivalentes\n\n"
for (file, line, column), rationale in EQUIVALENT.items():
    text += f"- `{file}:{line}:{column}`: {rationale}\n"
text += "\n## Os 43 originalmente não cobertos\n\n42 classificados como teste insuficiente, agora detectados; um equivalente. Nenhum inválido ou inconclusivo neste conjunto. Linhas históricas preservadas; spotify_rank.go ganhou quatro linhas antes dos pontos posteriores à linha 115. A falha do executor invalida os 178 KILLED antigos, mas não altera a identificação estática dos 43 pontos sem cobertura.\n\n"
text += "| Campanha | Local QA-008 | Local atual | Resultado | Classificação | Proteção comportamental |\n|---|---|---|---|---|---|\n"
for row in original_rows:
    text += f"| {row['campaign']} | {row['file']}:{row['old_line']}:{row['column']} | {row['current_line']}:{row['column']} | {row['status']} | {row['classification']} | {row['protection']} |\n"
text += "\n## Falhas intermediárias preservadas\n\nmutation-verified usa o executor corrigido com seleção reduzida de testes: revelou sobreviventes reais. mutation-complete usa todos os Test*, revelando 14 sobreviventes e um timeout. O timeout sqlite.go:92 ocorreu porque o mutante abandona uma transação sem rollback e o preparo da fixture aguardava outra conexão sem contexto. O preparo agora usa contexto e ExecContext: mutation-storage-final detecta a alteração com falha explícita, sem timeout.\n\nmutation-permissions-final falhou já no baseline: uma nova fixture tentava usar o hook Spotify Search numa resolução YouTube; substituída por executável yt-dlp controlado, sem alterar produção. mutation-permissions-reviewed passou no baseline e detectou todos os mutantes. Os relatórios intermediários e suas saídas individuais continuam preservados.\n\n"
text += "Reprodução: `python3 -B scripts/qa008_mutation.py --campaign <player|fidelity|storage|permissions> --test-pattern '^Test' --evidence-dir /tmp/qa009-mutation-review`. Os quatro relatórios finais podem usar seleções diferentes: player-final usa a seleção rápida padrão, depois de uma campanha completa, enquanto os demais usam todos os Test*.\n"
(REPORT / "mutation.md").write_text(text)
print(f"Reviewed {len(mutation_rows)} mutants; {len(original_rows)} original gaps; {killed} detected")
