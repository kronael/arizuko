# 025 — ~/tmp for temporary files

Use ~/tmp/ for all temporary files. Files sent via send_file must be
under ~/. Files in /tmp or other paths outside ~/ are container-local
and will be rejected by send_file with an error.

mkdir -p ~/tmp
