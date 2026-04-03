#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <context_growth.csv> [report.md]" >&2
  exit 1
fi

INPUT_CSV="$1"
OUT_FILE="${2:-}"
TIMEOUT_MS="${TIMEOUT_MS:-10000}"

if [[ ! -f "$INPUT_CSV" ]]; then
  echo "input csv not found: $INPUT_CSV" >&2
  exit 1
fi

tmp_out="$(mktemp)"

perl -MText::ParseWords -e '
  use strict;
  use warnings;

  my ($file, $timeout_ms) = @ARGV;
  open my $fh, "<", $file or die "open $file: $!";

  my $header = <$fh>;
  die "empty csv\n" unless defined $header;
  chomp $header;
  my @h = parse_line(",", 0, $header);
  my %idx;
  for my $i (0..$#h) { $idx{$h[$i]} = $i; }

  for my $required (qw(scenario size path latency_ms envelope_bytes request_body_bytes correctness compaction_stage selector_inclusion_reason selector_exclusion_reason selector_budget_pressure output_text)) {
    die "missing required column: $required\n" unless exists $idx{$required};
  }

  my %agg;
  my $rows = 0;
  my ($global_strict, $global_loose, $global_timeout, $global_extra, $global_non_ascii, $global_drift) = (0,0,0,0,0,0);

  while (my $line = <$fh>) {
    chomp $line;
    next if $line =~ /^\s*$/;
    my @f = parse_line(",", 0, $line);
    next unless @f >= @h;

    my $scenario = $f[$idx{scenario}];
    my $size = $f[$idx{size}];
    my $path = $f[$idx{path}];
    my $latency = 0 + ($f[$idx{latency_ms}] || 0);
    my $env = $f[$idx{envelope_bytes}];
    my $reqb = $f[$idx{request_body_bytes}];
    my $correctness = lc($f[$idx{correctness}] // "");
    my $stage = $f[$idx{compaction_stage}] // "n/a";
    my $selector_inclusion = $f[$idx{selector_inclusion_reason}] // "n/a";
    my $selector_exclusion = $f[$idx{selector_exclusion_reason}] // "n/a";
    my $selector_budget_pressure = 0 + ($f[$idx{selector_budget_pressure}] || 0);
    my $text = $f[$idx{output_text}] // "";
    $text =~ s/^\s+|\s+$//g;

    my $key = join("|", $scenario, $size, $path);
    my $a = ($agg{$key} ||= {
      scenario => $scenario, size => $size, path => $path,
      n => 0, lat => [],
      env_sum => 0, env_count => 0, env_max => 0,
      req_sum => 0, req_count => 0, req_max => 0,
      timeout => 0, strict_pass => 0, loose_pass => 0,
      extra_text => 0, non_ascii => 0, language_drift => 0,
      stage => {},
      selector_inclusion => {},
      selector_exclusion => {},
      selector_budget_pressure => 0,
    });
    $a->{n}++;
    push @{$a->{lat}}, $latency;

    if ($env ne "n/a" && $env =~ /^\d+$/) {
      $a->{env_sum} += $env;
      $a->{env_count}++;
      $a->{env_max} = $env if $env > $a->{env_max};
    }
    if ($reqb ne "n/a" && $reqb =~ /^\d+$/) {
      $a->{req_sum} += $reqb;
      $a->{req_count}++;
      $a->{req_max} = $reqb if $reqb > $a->{req_max};
    }
    if ($stage ne "" && $stage ne "n/a") {
      $a->{stage}{$stage}++;
    }
    if ($selector_inclusion ne "" && $selector_inclusion ne "n/a") {
      $a->{selector_inclusion}{$selector_inclusion}++;
    }
    if ($selector_exclusion ne "" && $selector_exclusion ne "n/a") {
      $a->{selector_exclusion}{$selector_exclusion}++;
    }
    $a->{selector_budget_pressure} += $selector_budget_pressure if $selector_budget_pressure > 0;

    my $expected_re = qr/BENCH_OK_\Q$scenario\E_\Q$size\E_\d+/;
    my $strict = ($text =~ /^$expected_re$/) ? 1 : 0;
    my $loose = ($correctness eq "pass" || $text =~ /$expected_re/i) ? 1 : 0;
    my $extra = ($loose && !$strict) ? 1 : 0;
    my $non_ascii = ($text =~ /[^\x00-\x7F]/) ? 1 : 0;
    my $drift = ($non_ascii || (!$loose && $text =~ /[A-Za-z]/)) ? 1 : 0;
    my $timeout = ($latency >= $timeout_ms) ? 1 : 0;

    $a->{strict_pass} += $strict;
    $a->{loose_pass} += $loose;
    $a->{extra_text} += $extra;
    $a->{non_ascii} += $non_ascii;
    $a->{language_drift} += $drift;
    $a->{timeout} += $timeout;

    $rows++;
    $global_strict += $strict;
    $global_loose += $loose;
    $global_extra += $extra;
    $global_non_ascii += $non_ascii;
    $global_drift += $drift;
    $global_timeout += $timeout;
  }
  close $fh;

  sub median {
    my (@v) = @_;
    return "n/a" unless @v;
    @v = sort { $a <=> $b } @v;
    my $n = scalar @v;
    return $v[int($n/2)] if $n % 2;
    return sprintf("%.1f", ($v[$n/2 - 1] + $v[$n/2]) / 2);
  }
  sub pct {
    my ($num, $den) = @_;
    return "n/a" if !$den;
    return sprintf("%.1f%%", 100.0 * $num / $den);
  }
  sub avg_or_na {
    my ($sum, $count) = @_;
    return "n/a" if !$count;
    return sprintf("%.1f", $sum / $count);
  }
  sub dominant_stage {
    my ($m) = @_;
    my $best = "n/a";
    my $bestn = -1;
    for my $k (sort keys %$m) {
      if ($m->{$k} > $bestn) {
        $best = $k; $bestn = $m->{$k};
      }
    }
    return $best;
  }

  print "# Context-Growth Benchmark Report\n\n";
  print "- Source CSV: `$file`\n";
  print "- Timeout threshold (ms): `$timeout_ms`\n";
  print "- Total rows: `$rows`\n";
  print "- Loose correctness pass rate: " . pct($global_loose, $rows) . "\n";
  print "- Strict correctness pass rate: " . pct($global_strict, $rows) . "\n";
  print "- Timeout count: `$global_timeout`\n";
  print "- Extra-text anomalies: `$global_extra`\n";
  print "- Non-ASCII anomalies: `$global_non_ascii`\n";
  print "- Possible language-drift anomalies: `$global_drift`\n\n";

  print "## Per Scenario/Size/Path\n\n";
  print "| scenario | size | path | n | median_latency_ms | timeout_count | loose_pass_rate | strict_pass_rate | avg_envelope_bytes | max_envelope_bytes | avg_request_body_bytes | max_request_body_bytes | dominant_compaction_stage | dominant_selector_inclusion | dominant_selector_exclusion | selector_budget_pressure | extra_text_anomalies | non_ascii_anomalies |\n";
  print "|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---|---|---:|---:|---:|\n";

  for my $key (sort {
    my ($as, $az, $ap) = split(/\|/, $a, 3);
    my ($bs, $bz, $bp) = split(/\|/, $b, 3);
    $as cmp $bs || $az <=> $bz || $ap cmp $bp
  } keys %agg) {
    my $x = $agg{$key};
    my $med = median(@{$x->{lat}});
    my $loose_rate = pct($x->{loose_pass}, $x->{n});
    my $strict_rate = pct($x->{strict_pass}, $x->{n});
    my $avg_env = avg_or_na($x->{env_sum}, $x->{env_count});
    my $max_env = $x->{env_count} ? $x->{env_max} : "n/a";
    my $avg_req = avg_or_na($x->{req_sum}, $x->{req_count});
    my $max_req = $x->{req_count} ? $x->{req_max} : "n/a";
    my $stage = dominant_stage($x->{stage});
    my $selector_in = dominant_stage($x->{selector_inclusion});
    my $selector_ex = dominant_stage($x->{selector_exclusion});
    print "| $x->{scenario} | $x->{size} | $x->{path} | $x->{n} | $med | $x->{timeout} | $loose_rate | $strict_rate | $avg_env | $max_env | $avg_req | $max_req | $stage | $selector_in | $selector_ex | $x->{selector_budget_pressure} | $x->{extra_text} | $x->{non_ascii} |\n";
  }
  print "\n";
' "$INPUT_CSV" "$TIMEOUT_MS" > "$tmp_out"

if [[ -n "$OUT_FILE" ]]; then
  cp "$tmp_out" "$OUT_FILE"
fi

cat "$tmp_out"
rm -f "$tmp_out"
