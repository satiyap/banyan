/* Says why the last sign-in was refused. */
(function () {
  const why = {
    invalid: 'That key is not valid, or it has already been used. Check it, or ask the treasurer for a new one.',
    expired: 'That key has expired. Ask the treasurer for a new one.',
    wait: 'Too many wrong keys. Wait ten minutes and try again.'
  }[new URLSearchParams(location.search).get('error')];
  if (!why) return;
  const note = document.getElementById('loginError');
  note.textContent = why;
  note.hidden = false;
})();
