# teleport-dev login profile — baked into the image at /etc/profile.d/ and sourced by
# ~/.bashrc (so both login and `kubectl exec` non-login shells get it). Edit here and
# rebuild the dev image; do NOT put durable shell config in the home PVC's .bashrc.

# Toolchain on PATH (reinforce — a stripped shell can otherwise miss go/cargo/node).
export PATH="/usr/local/cargo/bin:/usr/local/lib/nodejs-linux/bin:/opt/go/bin:/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:${WORKSPACE_DIR:-/workspace}/teleport/build"
export EDITOR=vim

# Prompt: user@teleport-dev:cwd (git-branch) $ — green user, magenta box, cyan cwd.
__tp_git_branch() { git rev-parse --abbrev-ref HEAD 2>/dev/null | sed 's/.*/ (&)/'; }
PS1='\[\e[32m\]\u\[\e[0m\]@\[\e[35m\]teleport-dev\[\e[0m\]:\[\e[36m\]\w\[\e[33m\]$(__tp_git_branch)\[\e[0m\]$ '

# Completions (only in interactive shells — these are a no-op cost otherwise).
case $- in
  *i*)
    [ -f /usr/share/bash-completion/bash_completion ] && . /usr/share/bash-completion/bash_completion
    command -v kubectl >/dev/null 2>&1 && { source <(kubectl completion bash); complete -o default -F __start_kubectl k; }
    command -v gh      >/dev/null 2>&1 && eval "$(gh completion -s bash)"
    ;;
esac

# Aliases
alias k=kubectl
alias ll='ls -lah'
alias la='ls -A'
alias gs='git status'
alias gl='git log --oneline -20'
alias gd='git diff'

# Drop new interactive shells into the repo (once per shell, not in subshells).
if [ -z "${__TP_CD_DONE:-}" ]; then
  cd "${WORKSPACE_DIR:-/workspace}/teleport" 2>/dev/null || true
  export __TP_CD_DONE=1
fi
