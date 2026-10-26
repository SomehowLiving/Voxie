// The navbar, on every page: the GitHub star count.
fetch("https://api.github.com/repos/SomehowLiving/Voxie")
  .then((r) => (r.ok ? r.json() : null))
  .then((repo) => {
    const n = repo?.stargazers_count;
    if (typeof n !== "number" || n < 1) return; // no badge until there is a star to show
    for (const el of document.querySelectorAll(".stars")) {
      el.textContent = n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n);
      el.hidden = false;
    }
  })
  .catch(() => {});
