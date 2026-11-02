// applyDiscount returns price reduced by percent (0-100), rounded to cents.
function applyDiscount(price, percent) {
  return Math.round(price * (1 - percent / 100) * 100) / 100;
}

module.exports = { applyDiscount };
